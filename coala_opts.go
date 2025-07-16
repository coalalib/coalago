package coalago

type Opt func(*coalaopts)

func WithPrivateKey(privatekey []byte) Opt {
	return func(opts *coalaopts) {
		opts.privatekey = privatekey
	}
}

type coalaopts struct {
	privatekey []byte
}
