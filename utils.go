package dag

import (
	"github.com/oarkflow/xid"
)

func NewID() string {
	return xid.New().String()
}
