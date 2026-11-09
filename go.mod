module exulte-app

go 1.24

require github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808

require (
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	golang.org/x/sys v0.5.0
)

replace (
	github.com/jchv/go-webview2 => ../deps/go-webview2
	github.com/jchv/go-winloader => ../deps/go-winloader
	golang.org/x/sys => ../deps/xsys
)
