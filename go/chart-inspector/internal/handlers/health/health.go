package health

import (
	"net/http"
	"sync/atomic"
)

func Live() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}

func Ready(ready *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if atomic.LoadInt32(ready) == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}

		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}
}
