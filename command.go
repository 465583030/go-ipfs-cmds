/*
Package commands provides an API for defining and parsing commands.

Supporting nested commands, options, arguments, etc.  The commands
package also supports a collection of marshallers for presenting
output to the user, including text, JSON, and XML marshallers.
*/

package cmds

import (
	"errors"
	"fmt"
	"io"

	"gx/ipfs/QmYiqbfRCkryYvJsxBopy77YEhxNZXTmq5Y2qiKyenc59C/go-ipfs-cmdkit"

	oldcmds "github.com/ipfs/go-ipfs/commands"
	"github.com/ipfs/go-ipfs/path"
	logging "gx/ipfs/QmSpJByNKFX1sCsHBEp3R73FL4NF6FnQTEGyNAXHm2GS52/go-log"
)

const DefaultOutputEncoding = JSON

var log = logging.Logger("cmds")

// Function is the type of function that Commands use.
// It reads from the Request, and writes results to the ResponseEmitter.
type Function func(Request, ResponseEmitter)

// PostRunMap is the map used in Command.PostRun.
type PostRunMap map[EncodingType]func(Request, ResponseEmitter) ResponseEmitter

// Command is a runnable command, with input arguments and options (flags).
// It can also have Subcommands, to group units of work into sets.
type Command struct {
	Options   []cmdsutil.Option
	Arguments []cmdsutil.Argument
	PreRun    func(req Request) error

	// Run is the function that processes the request to generate a response.
	// Note that when executing the command over the HTTP API you can only read
	// after writing when using multipart requests. The request body will not be
	// available for reading after the HTTP connection has been written to.
	Run      Function
	PostRun  PostRunMap
	Encoders map[EncodingType]func(Request) func(io.Writer) Encoder
	Helptext cmdsutil.HelpText

	// External denotes that a command is actually an external binary.
	// fewer checks and validations will be performed on such commands.
	External bool

	// Type describes the type of the output of the Command's Run Function.
	// In precise terms, the value of Type is an instance of the return type of
	// the Run Function.
	//
	// ie. If command Run returns &Block{}, then Command.Type == &Block{}
	Type           interface{}
	Subcommands    map[string]*Command
	OldSubcommands map[string]*oldcmds.Command
}

// ErrNotCallable signals a command that cannot be called.
var ErrNotCallable = ClientError("This command can't be called directly. Try one of its subcommands.")

var ErrNoFormatter = ClientError("This command cannot be formatted to plain text")

var ErrIncorrectType = errors.New("The command returned a value with a different type than expected")

// Call invokes the command for the given Request
func (c *Command) Call(req Request, re ResponseEmitter) error {
	defer re.Close()

	cmd, err := c.Get(req.Path())
	if err != nil {
		return err
	}

	if cmd.Run == nil {
		return ErrNotCallable
	}

	err = cmd.CheckArguments(req)
	if err != nil {
		return err
	}

	err = req.ConvertOptions()
	if err != nil {
		return err
	}

	// If this ResponseEmitter encodes messages (e.g. http, cli or writer - but not chan),
	// we need to update the encoding to the one specified by the command.
	if re_, ok := re.(EncodingEmitter); ok {
		encType := GetEncoding(req)

		if enc, ok := cmd.Encoders[EncodingType(encType)]; ok {
			re_.SetEncoder(enc(req))
			log.Debugf("updated encoder for type %s to %v", encType, enc)
		} else {
			log.Debugf("command has no encoder for %s", encType)
		}
	} else {
		log.Debugf("responseemitter is not an EncodingEmitter, but a %T", re)
	}

	log.Debugf("Call: calling cmd.Run %v", cmd.Run)
	cmd.Run(req, re)

	return nil
}

// Resolve returns the subcommands at the given path
func (c *Command) Resolve(pth []string) ([]*Command, error) {
	cmds := make([]*Command, len(pth)+1)
	cmds[0] = c

	cmd := c
	for i, name := range pth {
		cmd = cmd.Subcommand(name)

		if cmd == nil {
			pathS := path.Join(pth[:i])
			return nil, fmt.Errorf("Undefined command: '%s'", pathS)
		}

		cmds[i+1] = cmd
	}

	return cmds, nil
}

// Get resolves and returns the Command addressed by path
func (c *Command) Get(path []string) (*Command, error) {
	cmds, err := c.Resolve(path)
	if err != nil {
		return nil, err
	}
	return cmds[len(cmds)-1], nil
}

// GetOptions returns the options in the given path of commands
func (c *Command) GetOptions(path []string) (map[string]cmdsutil.Option, error) {
	options := make([]cmdsutil.Option, 0, len(c.Options))

	cmds, err := c.Resolve(path)
	if err != nil {
		return nil, err
	}
	cmds = append(cmds, globalCommand)

	for _, cmd := range cmds {
		options = append(options, cmd.Options...)
	}

	optionsMap := make(map[string]cmdsutil.Option)
	for _, opt := range options {
		for _, name := range opt.Names() {
			if _, found := optionsMap[name]; found {
				return nil, fmt.Errorf("Option name '%s' used multiple times", name)
			}

			optionsMap[name] = opt
		}
	}

	return optionsMap, nil
}

func (c *Command) CheckArguments(req Request) error {
	args := req.(*request).arguments

	// count required argument definitions
	numRequired := 0
	for _, argDef := range c.Arguments {
		if argDef.Required {
			numRequired++
		}
	}

	// iterate over the arg definitions
	valueIndex := 0 // the index of the current value (in `args`)
	for i, argDef := range c.Arguments {
		// skip optional argument definitions if there aren't
		// sufficient remaining values
		if len(args)-valueIndex <= numRequired && !argDef.Required ||
			argDef.Type == cmdsutil.ArgFile {
			continue
		}

		// the value for this argument definition. can be nil if it
		// wasn't provided by the caller
		v, found := "", false
		if valueIndex < len(args) {
			v = args[valueIndex]
			found = true
			valueIndex++
		}

		// in the case of a non-variadic required argument that supports stdin
		if !found && len(c.Arguments)-1 == i && argDef.SupportsStdin {
			found = true
		}

		err := checkArgValue(v, found, argDef)
		if err != nil {
			return err
		}

		// any additional values are for the variadic arg definition
		if argDef.Variadic && valueIndex < len(args)-1 {
			for _, val := range args[valueIndex:] {
				err := checkArgValue(val, true, argDef)
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// Subcommand returns the subcommand with the given id
func (c *Command) Subcommand(id string) *Command {
	cmd := c.Subcommands[id]
	if cmd != nil {
		return cmd
	}

	oldcmd := c.OldSubcommands[id]
	if oldcmd != nil {
		return NewCommand(oldcmd)
	}

	return nil
}

type CommandVisitor func(*Command)

// Walks tree of all subcommands (including this one)
func (c *Command) Walk(visitor CommandVisitor) {
	visitor(c)
	for _, cm := range c.Subcommands {
		cm.Walk(visitor)
	}
}

func (c *Command) ProcessHelp() {
	c.Walk(func(cm *Command) {
		ht := &cm.Helptext
		if len(ht.LongDescription) == 0 {
			ht.LongDescription = ht.ShortDescription
		}
	})
}

// checkArgValue returns an error if a given arg value is not valid for the
// given Argument
func checkArgValue(v string, found bool, def cmdsutil.Argument) error {
	if def.Variadic && def.SupportsStdin {
		return nil
	}

	if !found && def.Required {
		return fmt.Errorf("Argument '%s' is required", def.Name)
	}

	return nil
}

func ClientError(msg string) error {
	return &cmdsutil.Error{Code: cmdsutil.ErrClient, Message: msg}
}

// global options, added to every command
var globalOptions = []cmdsutil.Option{
	cmdsutil.OptionEncodingType,
	cmdsutil.OptionStreamChannels,
	cmdsutil.OptionTimeout,
}

// the above array of Options, wrapped in a Command
var globalCommand = &Command{
	Options: globalOptions,
}
