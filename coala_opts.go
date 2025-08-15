package coalago

type Opt func(*coalaopts)

func WithPrivateKey(privatekey []byte) Opt {
	return func(opts *coalaopts) {
		opts.privatekey = privatekey
	}
}

func WithExternalAddr(externalAddr string) Opt {
	return func(opts *coalaopts) {
		opts.externalAddr = externalAddr
	}
}

type coalaopts struct {
	privatekey   []byte
	externalAddr string
}
