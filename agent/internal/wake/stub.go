//go:build !cgo || !((darwin && (amd64 || arm64)) || (linux && (amd64 || arm64 || arm)) || (windows && (amd64 || arm64 || 386)))

package wake

type Detector struct{}

func New(Config) (*Detector, error)          { return nil, ErrUnavailable }
func (*Detector) Feed([]int16) (bool, error) { return false, ErrUnavailable }
func (*Detector) Reset()                     {}
func (*Detector) Close()                     {}
func (*Detector) LastMatch() Match           { return Match{} }
func (*Detector) KeywordEndSamples() int64   { return 0 }

type VAD struct{}

func NewVAD(string) (*VAD, error)         { return nil, ErrUnavailable }
func (*VAD) Speech([]int16) (bool, error) { return false, ErrUnavailable }
func (*VAD) Reset()                       {}
func (*VAD) Close()                       {}
