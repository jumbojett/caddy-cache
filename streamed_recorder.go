package cache

import (
	"github.com/mholt/caddy/caddyhttp/httpserver"
	"net/http"
)

/**
* Streamed Recorder is really similar to http.httpRecorder
* But this implementation is created with a ResponseWriter
* And sends the response to downstream at the same time
* the response is being recorded avoiding having to wait to
* have all the response.
 */

type HttpHeader struct {
	StatusCode int
	Header     *http.Header
}

type StreamedRecorder struct {
	handler     httpserver.Handler
	w           http.ResponseWriter // This is the downstream
	r           *http.Request
	wroteHeader bool

	bodyChannel   chan []byte
	headerChannel chan *HttpHeader
	endChannel    chan struct{}
}

func PipeHandlerToChannels(handler httpserver.Handler, w http.ResponseWriter, r *http.Request) *StreamedRecorder {
	return &StreamedRecorder{
		handler:       handler,
		w:             w,
		r:             r,
		wroteHeader:   false,
		bodyChannel:   make(chan []byte),
		headerChannel: make(chan *HttpHeader),
	}
}

func (rw *StreamedRecorder) handle() {
	rw.handler.ServeHTTP(rw, rw.r)
	//fmt.Println("Desde el recorder termino ServerHTTP, cerrando body channel")
	close(rw.bodyChannel)
}

// Header returns the response headers.
func (rw *StreamedRecorder) Header() http.Header {
	return rw.w.Header()
}

// Write always succeeds and writes to rw.Body, if not nil.
func (rw *StreamedRecorder) Write(buf []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(200)
	}
	//fmt.Println("Enviando contenido al bodyChannel, se bloqueo?")
	rw.bodyChannel <- buf
	//fmt.Println("Salio de enviar al bodychannel")
	return rw.w.Write(buf)
}

// WriteHeader sets rw.Code. After it is called, changing rw.Header
// will not affect rw.HeaderMap.
func (rw *StreamedRecorder) WriteHeader(code int) {
	if rw.wroteHeader {
		return
	}

	rw.wroteHeader = true
	rw.headerChannel <- &HttpHeader{
		StatusCode: code,
		Header:     cloneHeader(rw.w.Header()),
	}
	rw.w.WriteHeader(code)
}

func (rw *StreamedRecorder) HeaderChannel() <-chan *HttpHeader {
	return rw.headerChannel
}

func (rw *StreamedRecorder) BodyChannel() <-chan []byte {
	return rw.bodyChannel
}

func cloneHeader(h http.Header) *http.Header {
	h2 := make(http.Header, len(h))
	for k, vv := range h {
		vv2 := make([]string, len(vv))
		copy(vv2, vv)
		h2[k] = vv2
	}
	return &h2
}

func (rw *StreamedRecorder) Flush() {
	if _, ok := rw.w.(http.Flusher); ok {
		rw.w.(http.Flusher).Flush()
	}
}
