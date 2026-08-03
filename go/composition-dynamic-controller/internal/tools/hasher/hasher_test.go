package hasher

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkHashManySmallStrings(b *testing.B) {
	b.ReportAllocs()

	const numStrings = 100000
	const strSize = 64

	// Prepariamo i dati fuori dal timer
	data := make([]string, numStrings)
	for i := range data {
		data[i] = strings.Repeat("y", strSize)
	}

	b.SetBytes(int64(numStrings * strSize))

	h := NewFNVObjectHash()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		h.Reset()
		// Testiamo specificamente la funzione ottimizzata per varargs di stringhe
		if err := h.SumHashStrings(data...); err != nil {
			b.Fatalf("SumHashStrings failed: %v", err)
		}
	}
}

func BenchmarkHashLargeMap(b *testing.B) {
	b.ReportAllocs()

	// configurazione dei dati: tanti key/value con valori grandi
	const numKeys = 1000000
	const valSize = 1024 // bytes per valore

	largeVal := strings.Repeat("a", valSize)
	data := make(map[string]any, numKeys)
	for i := 0; i < numKeys; i++ {
		k := fmt.Sprintf("key-%06d", i)
		switch {
		case i%10 == 0:
			// map annidata
			data[k] = map[string]any{
				"sub": largeVal,
				"n":   i,
			}
		case i%3 == 0:
			// slice
			arr := make([]any, 0, 5)
			for j := 0; j < 5; j++ {
				arr = append(arr, largeVal)
			}
			data[k] = arr
		default:
			// stringa grande
			data[k] = largeVal
		}
	}

	// stima bytes processati per iterazione (utile per throughput)
	totalBytes := int64(numKeys * valSize)
	b.SetBytes(totalBytes)

	h := NewFNVObjectHash()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Reset()
		if err := h.SumHash(data); err != nil {
			b.Fatalf("SumHash failed: %v", err)
		}
	}
}

func BenchmarkHashLargeString(b *testing.B) {
	b.ReportAllocs()

	// Configurazione: una singola stringa enorme (es. 100MB)
	const strSize = 100 * 1024 * 1024 // 100 MB
	largeStr := strings.Repeat("x", strSize)

	// Impostiamo i bytes processati per calcolare il throughput (MB/s)
	b.SetBytes(int64(strSize))

	h := NewFNVObjectHash()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		h.Reset()
		// Qui testiamo SumHash con una stringa diretta.
		// Grazie all'ottimizzazione, questo dovrebbe evitare json.Marshal
		// e usare la conversione zero-copy.
		if err := h.SumHash(largeStr); err != nil {
			b.Fatalf("SumHash failed: %v", err)
		}
	}
}
func BenchmarkHash(b *testing.B) {
	input := []any{"test", 123, true, "another string", 456.78}

	h := NewFNVObjectHash()

	for i := 0; i < b.N; i++ {
		err := h.SumHash(input...)
		if err != nil {
			b.Fatalf("Hash() failed: %v", err)
		}
	}
}

func TestHash(t *testing.T) {
	tests := []struct {
		name    string
		input   []any
		wantErr bool
	}{
		{
			name: "Large map input",
			input: []any{
				map[string]any{
					"key1": "value1",
					"key2": 123,
					"key3": true,
					"key4": []any{"a", "b", "c"},
					"key5": map[string]any{
						"subkey1": "subvalue1",
						"subkey2": 456,
					},
				},
			},
			wantErr: false,
		},
		{
			name:    "Single string input",
			input:   []any{"test"},
			wantErr: false,
		},
		{
			name:    "Multiple inputs",
			input:   []any{"test", 123, true},
			wantErr: false,
		},
		{
			name:    "Empty input",
			input:   []any{},
			wantErr: false,
		},
		{
			name:    "Nil input",
			input:   nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewFNVObjectHash()
			err := h.SumHash(tt.input...)
			if (err != nil) != tt.wantErr {
				t.Errorf("Hash() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			got := h.GetHash()
			if got == "" && !tt.wantErr {
				t.Errorf("Hash() returned empty string, expected valid hash")
			}

			h2 := NewFNVObjectHash()
			err = h2.SumHash(tt.input...)
			if (err != nil) != tt.wantErr {
				t.Errorf("Hash() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			got2 := h2.GetHash()
			if got != got2 {
				t.Errorf("Hash() returned different hashes for same input: %s vs %s", got, got2)
			}
		})
	}
}
