package window

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

static NSWindow *hsWindow;

// The menu-bar app already runs the Cocoa main loop; work is sent to it.
static void hsOpen(const char *curl, int w, int h) {
	NSString *url = [NSString stringWithUTF8String:curl];
	dispatch_async(dispatch_get_main_queue(), ^{
		if (hsWindow == nil) {
			NSRect r = NSMakeRect(0, 0, w, h);
			hsWindow = [[NSWindow alloc] initWithContentRect:r
				styleMask:(NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable)
				backing:NSBackingStoreBuffered defer:NO];
			hsWindow.releasedWhenClosed = NO; // closing hides it; the app keeps running
			hsWindow.title = @"Helium Syncer";
			hsWindow.minSize = NSMakeSize(720, 520);
			WKWebView *web = [[WKWebView alloc] initWithFrame:r configuration:[WKWebViewConfiguration new]];
			hsWindow.contentView = web;
			[web loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:url]]];
			[hsWindow center];
		}
		[NSApp activateIgnoringOtherApps:YES];
		[hsWindow makeKeyAndOrderFront:nil];
	});
}

static void hsClose(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ [hsWindow orderOut:nil]; });
}
*/
import "C"

import "unsafe"

// Open shows the window with the page at url, or brings it to the front.
// dataDir is not used on macOS (WebKit keeps nothing that matters here).
func Open(url, dataDir string) error {
	c := C.CString(url)
	defer C.free(unsafe.Pointer(c))
	C.hsOpen(c, C.int(Width), C.int(Height))
	return nil
}

// Close hides the window.
func Close() { C.hsClose() }
