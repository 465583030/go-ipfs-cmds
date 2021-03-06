package cli

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync"

	"github.com/ipfs/go-ipfs-cmds"
	"gx/ipfs/QmYiqbfRCkryYvJsxBopy77YEhxNZXTmq5Y2qiKyenc59C/go-ipfs-cmdkit"
)

type ErrSet struct {
	error
}

func NewResponseEmitter(w io.WriteCloser, enc func(cmds.Request) func(io.Writer) cmds.Encoder, req cmds.Request) (cmds.ResponseEmitter, <-chan *cmdsutil.Error) {
	ch := make(chan *cmdsutil.Error)
	encType := cmds.GetEncoding(req)

	if enc == nil {
		enc = func(cmds.Request) func(io.Writer) cmds.Encoder {
			return func(io.Writer) cmds.Encoder {
				return nil
			}
		}
	}

	return &responseEmitter{w: w, encType: encType, enc: enc(req)(w), ch: ch}, ch
}

type responseEmitter struct {
	wLock sync.Mutex
	w     io.WriteCloser

	length  uint64
	err     *cmdsutil.Error
	enc     cmds.Encoder
	encType cmds.EncodingType

	ch chan<- *cmdsutil.Error
}

func (re *responseEmitter) SetLength(l uint64) {
	re.length = l
}

func (re *responseEmitter) SetEncoder(enc func(io.Writer) cmds.Encoder) {
	re.enc = enc(re.w)
}

func (re *responseEmitter) SetError(v interface{}, errType cmdsutil.ErrorType) error {
	log.Debugf("re.SetError(%v, %v)", v, errType)
	return re.Emit(&cmdsutil.Error{Message: fmt.Sprint(v), Code: errType})
}

func (re *responseEmitter) Close() error {
	re.wLock.Lock()
	defer re.wLock.Unlock()

	if re.w == nil {
		log.Warning("more than one call to RespEm.Close!")
		return nil
	}

	log.Debug("closing RE, err=", re.err)
	close(re.ch)
	log.Debug("re.ch closed.")
	if f, ok := re.w.(*os.File); ok {
		err := f.Sync()
		if err != nil {
			return err
		}
	}
	re.w = nil

	return nil
}

// Head returns the current head.
// TODO: maybe it makes sense to make these pointers to shared memory?
//   might not be so clever though...concurrency and stuff
func (re *responseEmitter) Head() cmds.Head {
	return cmds.Head{
		Len: re.length,
		Err: re.err,
	}
}

func (re *responseEmitter) Emit(v interface{}) error {
	if ch, ok := v.(chan interface{}); ok {
		v = (<-chan interface{})(ch)
	}

	if ch, isChan := v.(<-chan interface{}); isChan {
		for v = range ch {
			err := re.Emit(v)
			if err != nil {
				return err
			}
		}
		return nil
	}

	if v == nil {
		log.Debug(string(debug.Stack()))
	}
	log.Debugf("re.Emit(%T)", v)

	re.wLock.Lock()
	if re.w == nil {
		re.wLock.Unlock()
		return io.ErrClosedPipe
	}
	re.wLock.Unlock()

	if err, ok := v.(cmdsutil.Error); ok {
		log.Warningf("fixerr %s", debug.Stack())
		v = &err
	}

	var err error

	switch t := v.(type) {
	// send errors to the output channel so it will be printed and the program exits
	case *cmdsutil.Error:
		re.ch <- t
		return nil

	case io.Reader:
		var n int64

		log.Debug("case reader")
		log.Debug("start copying received reader to cli")
		n, err = io.Copy(re.w, t)
		if err != nil {
			re.SetError(err, cmdsutil.ErrNormal)
			err = nil
		}
		log.Debugf("done copying received reader to cli, n=%d, err=%s", n, err)
	default:
		log.Debug("case default")
		if re.enc != nil {
			log.Debug("using encoder")
			err = re.enc.Encode(v)
		} else {
			log.Debug("using fprintln")
			_, err = fmt.Fprintln(re.w, t)
		}
	}

	return err
}
