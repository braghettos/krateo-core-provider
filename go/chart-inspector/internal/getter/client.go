package getter

import (
	"k8s.io/client-go/dynamic"
)

type Client struct {
	dynamic dynamic.Interface
}

func NewClient(dynamic dynamic.Interface) *Client {
	return &Client{
		dynamic: dynamic,
	}
}
