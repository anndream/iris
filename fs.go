package iris

import (
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// StaticHandlerBuilder is the web file system's Handler builder
// use that or the iris.StaticHandler/StaticWeb methods
type StaticHandlerBuilder interface {
	Path(requestRoutePath string) StaticHandlerBuilder
	Gzip(enable bool) StaticHandlerBuilder
	Listing(listDirectoriesOnOff bool) StaticHandlerBuilder
	StripPath(yesNo bool) StaticHandlerBuilder
	Except(r ...RouteInfo) StaticHandlerBuilder
	Build() HandlerFunc
}

//  +------------------------------------------------------------+
//  |                                                            |
//  |                      Static Builder                        |
//  |                                                            |
//  +------------------------------------------------------------+

type fsHandler struct {
	// user options, only directory is required.
	directory       http.Dir
	requestPath     string
	stripPath       bool
	gzip            bool
	listDirectories bool
	// these are init on the Build() call
	filesystem http.FileSystem
	once       sync.Once
	exceptions []RouteInfo
	handler    HandlerFunc
}

func toWebPath(systemPath string) string {
	// winos slash to slash
	webpath := strings.Replace(systemPath, "\\", slash, -1)
	// double slashes to single
	webpath = strings.Replace(webpath, slash+slash, slash, -1)
	// remove all dots
	webpath = strings.Replace(webpath, ".", "", -1)
	return webpath
}

// abs calls filepath.Abs but ignores the error and
// returns the original value if any error occurred.
func abs(path string) string {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absPath
}

// NewStaticHandlerBuilder returns a new Handler which serves static files
// supports gzip, no listing and much more
// Note that, this static builder returns a Handler
// it doesn't cares about the rest of your iris configuration.
//
// Use the iris.StaticHandler/StaticWeb in order to serve static files on more automatic way
// this builder is used by people who have more complicated application
// structure and want a fluent api to work on.
func NewStaticHandlerBuilder(dir string) StaticHandlerBuilder {
	return &fsHandler{
		directory: http.Dir(abs(dir)),
		// default route path is the same as the directory
		requestPath: toWebPath(dir),
		// enable strip path by-default
		stripPath: true,
		// gzip is disabled by default
		gzip: false,
		// list directories disabled by default
		listDirectories: false,
	}
}

// Path sets the request path.
// Defaults to same as system path
func (w *fsHandler) Path(requestRoutePath string) StaticHandlerBuilder {
	w.requestPath = toWebPath(requestRoutePath)
	return w
}

// Gzip if enable is true then gzip compression is enabled for this static directory
// Defaults to false
func (w *fsHandler) Gzip(enable bool) StaticHandlerBuilder {
	w.gzip = enable
	return w
}

// Listing turn on/off the 'show files and directories'.
// Defaults to false
func (w *fsHandler) Listing(listDirectoriesOnOff bool) StaticHandlerBuilder {
	w.listDirectories = listDirectoriesOnOff
	return w
}

func (w *fsHandler) StripPath(yesNo bool) StaticHandlerBuilder {
	w.stripPath = yesNo
	return w
}

// Except add a route exception,
// gives priority to that Route over the static handler.
func (w *fsHandler) Except(r ...RouteInfo) StaticHandlerBuilder {
	w.exceptions = append(w.exceptions, r...)
	return w
}

type (
	noListFile struct {
		http.File
	}
)

// Overrides the Readdir of the http.File in order to disable showing a list of the dirs/files
func (n noListFile) Readdir(count int) ([]os.FileInfo, error) {
	return nil, nil
}

// Implements the http.Filesystem
// Do not call it.
func (w *fsHandler) Open(name string) (http.File, error) {
	info, err := w.filesystem.Open(name)

	if err != nil {
		return nil, err
	}

	if !w.listDirectories {
		return noListFile{info}, nil
	}

	return info, nil
}

// Build the handler (once) and returns it
func (w *fsHandler) Build() HandlerFunc {
	// we have to ensure that Build is called ONLY one time,
	// one instance per one static directory.
	w.once.Do(func() {
		w.filesystem = w.directory

		// set the filesystem to itself in order to be recognised of listing property (can be change at runtime too)
		fileserver := http.FileServer(w)
		fsHandler := fileserver
		if w.stripPath {
			prefix := w.requestPath
			fsHandler = http.StripPrefix(prefix, fileserver)
		}

		h := func(ctx *Context) {
			writer := ctx.ResponseWriter

			if w.gzip && ctx.clientAllowsGzip() {
				ctx.ResponseWriter.Header().Add(varyHeader, acceptEncodingHeader)
				ctx.SetHeader(contentEncodingHeader, "gzip")
				gzipResWriter := acquireGzipResponseWriter(ctx.ResponseWriter) //.ResponseWriter)
				writer = gzipResWriter
				defer releaseGzipResponseWriter(gzipResWriter)
			}

			fsHandler.ServeHTTP(writer, ctx.Request)
		}

		if len(w.exceptions) > 0 {
			middleware := make(Middleware, len(w.exceptions)+1)
			for i := range w.exceptions {
				middleware[i] = Prioritize(w.exceptions[i])
			}
			middleware[len(w.exceptions)] = HandlerFunc(h)

			w.handler = func(ctx *Context) {
				ctx.Middleware = append(middleware, ctx.Middleware...)
				ctx.Do()
			}
		} else {
			w.handler = h
		}
	})

	return w.handler
}

// StripPrefix returns a handler that serves HTTP requests
// by removing the given prefix from the request URL's Path
// and invoking the handler h. StripPrefix handles a
// request for a path that doesn't begin with prefix by
// replying with an HTTP 404 not found error.
func StripPrefix(prefix string, h HandlerFunc) HandlerFunc {
	if prefix == "" {
		return h
	}
	return func(ctx *Context) {
		if p := strings.TrimPrefix(ctx.Request.URL.Path, prefix); len(p) < len(ctx.Request.URL.Path) {
			ctx.Request.URL.Path = p
			h(ctx)
		} else {
			ctx.NotFound()
		}
	}
}

// typeByExtension returns the MIME type associated with the file extension ext.
// The extension ext should begin with a leading dot, as in ".html".
// When ext has no associated type, typeByExtension returns "".
//
// Extensions are looked up first case-sensitively, then case-insensitively.
//
// The built-in table is small but on unix it is augmented by the local
// system's mime.types file(s) if available under one or more of these
// names:
//
//   /etc/mime.types
//   /etc/apache2/mime.types
//   /etc/apache/mime.types
//
// On Windows, MIME types are extracted from the registry.
//
// Text types have the charset parameter set to "utf-8" by default.
func typeByExtension(fullfilename string) (t string) {
	ext := filepath.Ext(fullfilename)
	//these should be found by the windows(registry) and unix(apache) but on windows some machines have problems on this part.
	if t = mime.TypeByExtension(ext); t == "" {
		// no use of map here because we will have to lock/unlock it, by hand is better, no problem:
		if ext == ".json" {
			t = "application/json"
		} else if ext == ".js" {
			t = "application/javascript"
		} else if ext == ".zip" {
			t = "application/zip"
		} else if ext == ".3gp" {
			t = "video/3gpp"
		} else if ext == ".7z" {
			t = "application/x-7z-compressed"
		} else if ext == ".ace" {
			t = "application/x-ace-compressed"
		} else if ext == ".aac" {
			t = "audio/x-aac"
		} else if ext == ".ico" { // for any case
			t = "image/x-icon"
		} else if ext == ".png" {
			t = "image/png"
		} else {
			t = "application/octet-stream"
		}
		// mime.TypeByExtension returns as text/plain; | charset=utf-8 the static .js (not always)
	} else if t == "text/plain" || t == "text/plain; charset=utf-8" {
		if ext == ".js" {
			t = "application/javascript"
		}
	}
	return
}

// directoryExists returns true if a directory(or file) exists, otherwise false
func directoryExists(dir string) bool {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return false
	}
	return true
}
