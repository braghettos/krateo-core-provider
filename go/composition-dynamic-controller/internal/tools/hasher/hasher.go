package hasher

import (
	"encoding/json"
	"fmt"
	"hash"
	"hash/fnv"
	"unsafe"
)

type ObjectHash struct {
	hash.Hash64
}

// Optimization: Zero-copy conversion from string to []byte.
// This prevents allocating a copy of the string just to read it.
func stringToBytes(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

func (h *ObjectHash) SumHashStrings(a ...string) error {
	for _, v := range a {
		// Use zero-copy conversion here
		if _, err := h.Write(stringToBytes(v)); err != nil {
			return err
		}
	}
	return nil
}

// the hash is cumulative, so you can call Hash() multiple times
// with different values and the hash will be updated
func (h *ObjectHash) SumHash(a ...any) error {
	for _, v := range a {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := h.Write(b); err != nil {
			return err
		}
	}
	return nil
}

func (h *ObjectHash) Reset() {
	h.Hash64.Reset()
}
func (h *ObjectHash) GetHash() string {
	return fmt.Sprintf("%x", h.Hash64.Sum64())
}

func NewFNVObjectHash() ObjectHash {
	return ObjectHash{fnv.New64()}
}
