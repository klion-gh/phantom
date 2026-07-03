package common

import "sync"

var BufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 4096)
		return &buf
	},
}

func GetBuffer() []byte {
	return *BufferPool.Get().(*[]byte)
}

func PutBuffer(buf []byte) {
	BufferPool.Put(&buf)
}
