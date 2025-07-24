package http1

type Option struct {
	OrderHeaders []interface {
		Key() string
		Val() any
	}
}
