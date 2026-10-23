package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRecoveryPreservesTransportAbortAndIsolatesOtherPanics(t *testing.T) {
	for _, abort := range []bool{false, true} {
		func() {
			router := gin.New()
			router.Use(RecoverHTTP())
			router.GET("/", func(c *gin.Context) {
				if abort {
					panic(http.ErrAbortHandler)
				}
				panic("private panic value")
			})
			recorder := httptest.NewRecorder()
			defer func() {
				failure := recover()
				if abort {
					if failure != http.ErrAbortHandler {
						t.Fatal("transport abort swallowed", failure)
					}
				} else if failure != nil || recorder.Code != 500 {
					t.Fatal("ordinary panic escaped", failure, recorder.Code)
				}
			}()
			router.ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
		}()
	}
}
