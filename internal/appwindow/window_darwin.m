//go:build darwin && cgo

// Native window of the LiteZap app: NSApplication delegate, menus, one WKWebView window
// and a status view shown until the dashboard loads. See appwindow.go.

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include "window_darwin.h"
#include "_cgo_export.h"

@interface LZController : NSObject <NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate,
	WKUIDelegate, WKScriptMessageHandler, WKDownloadDelegate>
@property(strong) NSWindow *window;
@property(strong) WKWebView *webView;
@property(strong) NSView *statusView;
@property(strong) NSProgressIndicator *spinner;
@property(strong) NSTextField *statusLabel;
@property(strong) NSButton *logButton;
@property(strong) NSString *origin; // scheme://host:port of the dashboard
@property(strong) NSMutableArray<NSWindow *> *popups;
@property BOOL hidden;      // start hidden
@property BOOL pageShown;   // the web view replaced the status view
@property BOOL failed;      // an error is shown: a late page load must not hide it
@property BOOL quitPending; // applicationShouldTerminate answered NSTerminateLater
@property BOOL quitOK;      // Go said the engine stopped: terminate for real
@end

static LZController *lz;

// str returns @"" for NULL or invalid UTF-8 (stringWithUTF8String: answers nil then).
static NSString *str(const char *s) { return (s ? [NSString stringWithUTF8String:s] : nil) ?: @""; }

// originOf is scheme://host:port with the default port spelled out, so
// http://127.0.0.1:8787 and http://127.0.0.1:8787/app/ compare equal.
static NSString *originOf(NSURL *u) {
	NSString *scheme = u.scheme.lowercaseString;
	if (!u.host || !([scheme isEqualToString:@"http"] || [scheme isEqualToString:@"https"])) return nil;
	NSNumber *port = u.port ?: ([scheme isEqualToString:@"https"] ? @443 : @80);
	return [NSString stringWithFormat:@"%@://%@:%@", scheme, u.host.lowercaseString, port];
}

static BOOL openExternally(NSURL *u) {
	NSString *s = u.scheme.lowercaseString;
	if (!([s isEqualToString:@"http"] || [s isEqualToString:@"https"] || [s isEqualToString:@"mailto"] ||
	      [s isEqualToString:@"tel"])) {
		return NO;
	}
	return [[NSWorkspace sharedWorkspace] openURL:u];
}

static NSMenuItem *addItem(NSMenu *m, NSString *title, SEL action, NSString *key, id target, NSInteger tag) {
	NSMenuItem *it = [[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key ?: @""];
	it.target = target;
	it.tag = tag;
	[m addItem:it];
	return it;
}

static void addSubmenu(NSMenu *bar, NSMenu *sub) {
	NSMenuItem *it = [[NSMenuItem alloc] initWithTitle:sub.title action:nil keyEquivalent:@""];
	it.submenu = sub;
	[bar addItem:it];
}

@implementation LZController

// ---------- application ----------

- (void)buildMenus {
	NSMenu *bar = [[NSMenu alloc] init];
	NSString *name = @"LiteZap";

	NSMenu *app = [[NSMenu alloc] initWithTitle:name];
	addItem(app, [@"Sobre o " stringByAppendingString:name], @selector(orderFrontStandardAboutPanel:), nil, NSApp, 0);
	[app addItem:[NSMenuItem separatorItem]];
	addItem(app, [@"Ocultar " stringByAppendingString:name], @selector(hide:), @"h", NSApp, 0);
	NSMenuItem *others = addItem(app, @"Ocultar outros", @selector(hideOtherApplications:), @"h", NSApp, 0);
	others.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagOption;
	addItem(app, @"Mostrar todos", @selector(unhideAllApplications:), nil, NSApp, 0);
	[app addItem:[NSMenuItem separatorItem]];
	addItem(app, [@"Sair do " stringByAppendingString:name], @selector(terminate:), @"q", NSApp, 0);
	addSubmenu(bar, app);

	// Without an Edit menu, Cmd+C/V/X/A do nothing in the web view's text fields.
	NSMenu *edit = [[NSMenu alloc] initWithTitle:@"Editar"];
	addItem(edit, @"Desfazer", @selector(undo:), @"z", nil, 0);
	NSMenuItem *redo = addItem(edit, @"Refazer", @selector(redo:), @"z", nil, 0);
	redo.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagShift;
	[edit addItem:[NSMenuItem separatorItem]];
	addItem(edit, @"Recortar", @selector(cut:), @"x", nil, 0);
	addItem(edit, @"Copiar", @selector(copy:), @"c", nil, 0);
	addItem(edit, @"Colar", @selector(paste:), @"v", nil, 0);
	addItem(edit, @"Selecionar tudo", @selector(selectAll:), @"a", nil, 0);
	addSubmenu(bar, edit);

	NSMenu *view = [[NSMenu alloc] initWithTitle:@"Visualizar"];
	addItem(view, @"Recarregar painel", @selector(goMenu:), @"r", self, 1);
	addItem(view, @"Abrir no navegador", @selector(goMenu:), nil, self, 2);
	addSubmenu(bar, view);

	NSMenu *win = [[NSMenu alloc] initWithTitle:@"Janela"];
	addItem(win, @"Minimizar", @selector(performMiniaturize:), @"m", nil, 0);
	addItem(win, @"Fechar janela", @selector(performClose:), @"w", nil, 0);
	[win addItem:[NSMenuItem separatorItem]];
	addItem(win, @"Painel do LiteZap", @selector(showWindow:), @"0", self, 0);
	addSubmenu(bar, win);
	NSApp.windowsMenu = win;

	NSMenu *help = [[NSMenu alloc] initWithTitle:@"Ajuda"];
	addItem(help, @"Abrir pasta de dados", @selector(goMenu:), nil, self, 3);
	addItem(help, @"Ver log", @selector(goMenu:), nil, self, 4);
	addSubmenu(bar, help);
	NSApp.helpMenu = help;

	NSApp.mainMenu = bar;
}

- (NSMenu *)applicationDockMenu:(NSApplication *)sender {
	NSMenu *m = [[NSMenu alloc] init];
	addItem(m, @"Abrir o painel", @selector(showWindow:), nil, self, 0);
	addItem(m, @"Conectar a IA", @selector(goMenu:), nil, self, 5);
	addItem(m, @"Abrir no navegador", @selector(goMenu:), nil, self, 2);
	return m;
}

- (void)goMenu:(NSMenuItem *)sender {
	if (sender.tag == 5) [self showWindow:nil];
	lzwMenu((int)sender.tag);
}

- (void)applicationDidFinishLaunching:(NSNotification *)n {
	if (!self.hidden) [self showWindow:nil];
	lzwReady();
}

// Dock icon clicked, or the app opened again from Finder/Launchpad.
- (BOOL)applicationShouldHandleReopen:(NSApplication *)sender hasVisibleWindows:(BOOL)visible {
	[self showWindow:nil];
	return NO;
}

- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)sender {
	return NO; // closing the window keeps the engine (WhatsApp, MCP) running
}

// Quitting waits for Go to stop the engine (sessions closed cleanly) and call lzw_finish.
- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)sender {
	if (self.quitOK) return NSTerminateNow;
	if (!self.quitPending) {
		self.quitPending = YES;
		[self.window orderOut:nil];
		for (NSWindow *w in [self.popups copy]) [w close];
		lzwQuit();
	}
	return NSTerminateLater;
}

- (void)showWindow:(id)sender {
	if (self.quitPending) return;
	if (self.window.isMiniaturized) [self.window deminiaturize:nil];
	[self.window makeKeyAndOrderFront:nil];
	[NSApp activateIgnoringOtherApps:YES];
}

// ---------- window ----------

- (void)setupWindow:(NSString *)title width:(int)w height:(int)h loading:(NSString *)loading {
	NSRect frame = NSMakeRect(0, 0, w, h);
	NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable |
	                          NSWindowStyleMaskResizable;
	self.window = [[NSWindow alloc] initWithContentRect:frame styleMask:style backing:NSBackingStoreBuffered defer:NO];
	self.window.title = title;
	self.window.releasedWhenClosed = NO;
	self.window.delegate = self;
	self.window.minSize = NSMakeSize(720, 520);
	self.window.tabbingMode = NSWindowTabbingModeDisallowed;
	[self.window center];
	[self.window setFrameAutosaveName:@"LiteZapMain"]; // remembers size and position

	NSView *content = self.window.contentView;

	WKWebViewConfiguration *cfg = [[WKWebViewConfiguration alloc] init];
	[cfg.userContentController addScriptMessageHandler:self name:@"litezap"];
	cfg.preferences.javaScriptCanOpenWindowsAutomatically = YES;
	self.webView = [[WKWebView alloc] initWithFrame:content.bounds configuration:cfg];
	self.webView.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
	self.webView.navigationDelegate = self;
	self.webView.UIDelegate = self;
	self.webView.hidden = YES;
	[content addSubview:self.webView];

	// Status view: spinner and a message, centered.
	self.statusView = [[NSView alloc] initWithFrame:content.bounds];
	self.statusView.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
	self.spinner = [[NSProgressIndicator alloc] init];
	self.spinner.style = NSProgressIndicatorStyleSpinning;
	self.spinner.controlSize = NSControlSizeRegular;
	self.spinner.translatesAutoresizingMaskIntoConstraints = NO;
	[self.spinner startAnimation:nil];
	self.statusLabel = [NSTextField wrappingLabelWithString:loading];
	self.statusLabel.alignment = NSTextAlignmentCenter;
	self.statusLabel.font = [NSFont systemFontOfSize:15];
	self.statusLabel.textColor = NSColor.secondaryLabelColor;
	self.statusLabel.selectable = YES;
	self.statusLabel.translatesAutoresizingMaskIntoConstraints = NO;
	self.logButton = [NSButton buttonWithTitle:@"Ver log" target:self action:@selector(goMenu:)];
	self.logButton.tag = 4;
	self.logButton.hidden = YES;
	self.logButton.translatesAutoresizingMaskIntoConstraints = NO;
	NSStackView *stack = [NSStackView stackViewWithViews:@[ self.spinner, self.statusLabel, self.logButton ]];
	stack.orientation = NSUserInterfaceLayoutOrientationVertical;
	stack.spacing = 16;
	stack.translatesAutoresizingMaskIntoConstraints = NO;
	[self.statusView addSubview:stack];
	[NSLayoutConstraint activateConstraints:@[
		[stack.centerXAnchor constraintEqualToAnchor:self.statusView.centerXAnchor],
		[stack.centerYAnchor constraintEqualToAnchor:self.statusView.centerYAnchor],
		[self.statusLabel.widthAnchor constraintLessThanOrEqualToConstant:520],
	]];
	[content addSubview:self.statusView];
	self.popups = [NSMutableArray array];
}

- (void)setStatus:(NSString *)text error:(BOOL)isError {
	self.pageShown = NO;
	self.failed = isError;
	self.webView.hidden = YES;
	self.statusView.hidden = NO;
	self.statusLabel.stringValue = text;
	self.statusLabel.textColor = isError ? NSColor.labelColor : NSColor.secondaryLabelColor;
	self.spinner.hidden = isError;
	self.logButton.hidden = !isError;
	if (isError) {
		[self.spinner stopAnimation:nil];
	} else {
		[self.spinner startAnimation:nil];
	}
}

- (void)showPage {
	if (self.pageShown || self.failed) return;
	self.pageShown = YES;
	self.statusView.hidden = YES;
	[self.spinner stopAnimation:nil];
	self.webView.hidden = NO;
	[self.window makeFirstResponder:self.webView];
}

// The close button hides the window: the engine keeps running for WhatsApp and the MCP.
- (BOOL)windowShouldClose:(NSWindow *)sender {
	if (sender == self.window) {
		[sender orderOut:nil];
		return NO;
	}
	return YES;
}

- (void)windowWillClose:(NSNotification *)n {
	NSWindow *w = n.object;
	if (w != self.window) [self.popups removeObject:w];
}

// ---------- navigation ----------

- (BOOL)isDashboard:(NSURL *)u {
	NSString *o = originOf(u);
	return o && self.origin && [o isEqualToString:self.origin];
}

// isOurs: a dashboard URL, or a blob: URL created by the dashboard (blob:http://127.0.0.1:8787/<uuid>).
- (BOOL)isOurs:(NSURL *)u {
	if ([u.scheme.lowercaseString isEqualToString:@"blob"]) {
		NSString *inner = [u.absoluteString substringFromIndex:5];
		return [self isDashboard:[NSURL URLWithString:inner]];
	}
	return [self isDashboard:u];
}

- (void)webView:(WKWebView *)webView
	decidePolicyForNavigationAction:(WKNavigationAction *)action
	                decisionHandler:(void (^)(WKNavigationActionPolicy))decide {
	NSURL *u = action.request.URL;
	NSString *scheme = u.scheme.lowercaseString;
	BOOL ours = [self isOurs:u];
	if (action.shouldPerformDownload) {
		// Downloads only from the dashboard (and its blob: URLs); anything else is the browser's.
		if (ours) {
			decide(WKNavigationActionPolicyDownload);
		} else {
			openExternally(u);
			decide(WKNavigationActionPolicyCancel);
		}
		return;
	}
	if (ours || [scheme isEqualToString:@"about"]) {
		decide(WKNavigationActionPolicyAllow);
		return;
	}
	// Anything else leaves the app: the default browser (main frame only; never load
	// another site inside the window that holds the admin session).
	if (action.targetFrame == nil || action.targetFrame.isMainFrame) openExternally(u);
	decide(WKNavigationActionPolicyCancel);
}

- (void)webView:(WKWebView *)webView
	decidePolicyForNavigationResponse:(WKNavigationResponse *)response
	                  decisionHandler:(void (^)(WKNavigationResponsePolicy))decide {
	if (response.canShowMIMEType) {
		decide(WKNavigationResponsePolicyAllow);
		return;
	}
	NSURL *u = response.response.URL;
	BOOL ours = [self isOurs:u];
	decide(ours ? WKNavigationResponsePolicyDownload : WKNavigationResponsePolicyCancel);
}

- (void)webView:(WKWebView *)webView didFinishNavigation:(WKNavigation *)navigation {
	if (webView == self.webView && self.origin) [self showPage];
}

- (void)webView:(WKWebView *)webView didFailProvisionalNavigation:(WKNavigation *)navigation withError:(NSError *)error {
	if (webView != self.webView || error.code == NSURLErrorCancelled) return;
	if ([error.domain isEqualToString:@"WebKitErrorDomain"] && error.code == 102) return; // frame load interrupted (download)
	[self setStatus:[NSString stringWithFormat:@"Não foi possível abrir o painel: %@\n\nUse Visualizar > Recarregar painel (⌘R).",
	                                           error.localizedDescription]
	          error:YES];
}

- (void)webViewWebContentProcessDidTerminate:(WKWebView *)webView {
	if (webView == self.webView) lzwMenu(1); // reload with a fresh login
}

// target=_blank and window.open: other sites go to the browser; the dashboard's own
// pages (and blob: media) open in a separate window that shares the session.
- (WKWebView *)webView:(WKWebView *)webView
	createWebViewWithConfiguration:(WKWebViewConfiguration *)cfg
	           forNavigationAction:(WKNavigationAction *)action
	                windowFeatures:(WKWindowFeatures *)features {
	NSURL *u = action.request.URL;
	NSString *scheme = u.scheme.lowercaseString;
	if (!([self isOurs:u] || [scheme isEqualToString:@"about"])) {
		openExternally(u);
		return nil;
	}
	NSRect frame = NSMakeRect(0, 0, 900, 700);
	NSWindow *w = [[NSWindow alloc] initWithContentRect:frame
	                                          styleMask:self.window.styleMask
	                                            backing:NSBackingStoreBuffered
	                                              defer:NO];
	w.releasedWhenClosed = NO;
	w.delegate = self;
	w.title = @"LiteZap";
	WKWebView *wv = [[WKWebView alloc] initWithFrame:w.contentView.bounds configuration:cfg];
	wv.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
	wv.navigationDelegate = self;
	wv.UIDelegate = self;
	[w.contentView addSubview:wv];
	[w cascadeTopLeftFromPoint:NSMakePoint(NSMinX(self.window.frame) + 24, NSMaxY(self.window.frame) - 24)];
	[self.popups addObject:w];
	[w makeKeyAndOrderFront:nil];
	return wv;
}

- (void)webViewDidClose:(WKWebView *)webView {
	if (webView != self.webView) [webView.window close];
}

// ---------- JavaScript dialogs, file picker ----------

- (void)webView:(WKWebView *)webView
	runJavaScriptAlertPanelWithMessage:(NSString *)message
	                  initiatedByFrame:(WKFrameInfo *)frame
	                 completionHandler:(void (^)(void))done {
	NSAlert *a = [[NSAlert alloc] init];
	a.messageText = message;
	[a addButtonWithTitle:@"OK"];
	[a runModal];
	done();
}

- (void)webView:(WKWebView *)webView
	runJavaScriptConfirmPanelWithMessage:(NSString *)message
	                    initiatedByFrame:(WKFrameInfo *)frame
	                   completionHandler:(void (^)(BOOL))done {
	NSAlert *a = [[NSAlert alloc] init];
	a.messageText = message;
	a.alertStyle = NSAlertStyleWarning;
	[a addButtonWithTitle:@"OK"];
	[a addButtonWithTitle:@"Cancelar"];
	done([a runModal] == NSAlertFirstButtonReturn);
}

- (void)webView:(WKWebView *)webView
	runJavaScriptTextInputPanelWithPrompt:(NSString *)prompt
	                          defaultText:(NSString *)defaultText
	                     initiatedByFrame:(WKFrameInfo *)frame
	                    completionHandler:(void (^)(NSString *))done {
	NSAlert *a = [[NSAlert alloc] init];
	a.messageText = prompt;
	[a addButtonWithTitle:@"OK"];
	[a addButtonWithTitle:@"Cancelar"];
	NSTextField *input = [[NSTextField alloc] initWithFrame:NSMakeRect(0, 0, 300, 24)];
	input.stringValue = defaultText ?: @"";
	a.accessoryView = input;
	a.window.initialFirstResponder = input;
	done([a runModal] == NSAlertFirstButtonReturn ? input.stringValue : nil);
}

- (void)webView:(WKWebView *)webView
	runOpenPanelWithParameters:(WKOpenPanelParameters *)params
	          initiatedByFrame:(WKFrameInfo *)frame
	         completionHandler:(void (^)(NSArray<NSURL *> *))done {
	NSOpenPanel *p = [NSOpenPanel openPanel];
	p.canChooseFiles = YES;
	p.canChooseDirectories = params.allowsDirectories;
	p.allowsMultipleSelection = params.allowsMultipleSelection;
	void (^finish)(NSModalResponse) = ^(NSModalResponse r) {
		done(r == NSModalResponseOK ? p.URLs : nil);
	};
	if (webView.window.isVisible) {
		[p beginSheetModalForWindow:webView.window completionHandler:finish];
	} else {
		finish([p runModal]);
	}
}

// ---------- downloads ("Baixar" in the dashboard) ----------

- (void)webView:(WKWebView *)webView navigationAction:(WKNavigationAction *)a didBecomeDownload:(WKDownload *)d {
	d.delegate = self;
}

- (void)webView:(WKWebView *)webView navigationResponse:(WKNavigationResponse *)r didBecomeDownload:(WKDownload *)d {
	d.delegate = self;
}

- (void)download:(WKDownload *)download
	decideDestinationUsingResponse:(NSURLResponse *)response
	             suggestedFilename:(NSString *)name
	             completionHandler:(void (^)(NSURL *))done {
	NSSavePanel *p = [NSSavePanel savePanel];
	p.nameFieldStringValue = name.length ? name : @"arquivo";
	p.directoryURL = [[NSFileManager defaultManager] URLsForDirectory:NSDownloadsDirectory inDomains:NSUserDomainMask].firstObject;
	NSWindow *parent = self.window.isVisible ? self.window : nil;
	void (^finish)(NSModalResponse) = ^(NSModalResponse r) {
		if (r != NSModalResponseOK || !p.URL) {
			done(nil);
			return;
		}
		// The panel already asked before replacing; WKDownload refuses an existing file.
		[[NSFileManager defaultManager] removeItemAtURL:p.URL error:nil];
		done(p.URL);
	};
	if (parent) {
		[p beginSheetModalForWindow:parent completionHandler:finish];
	} else {
		finish([p runModal]);
	}
}

- (void)download:(WKDownload *)download didFailWithError:(NSError *)error resumeData:(NSData *)data {
	if (error.code == NSURLErrorCancelled) return;
	NSAlert *a = [[NSAlert alloc] init];
	a.messageText = @"Não foi possível baixar o arquivo";
	a.informativeText = error.localizedDescription;
	[a runModal];
}

// ---------- messages from the dashboard ----------

- (void)userContentController:(WKUserContentController *)ucc didReceiveScriptMessage:(WKScriptMessage *)msg {
	// Only the main dashboard page, and only the one message the app knows.
	if (msg.webView != self.webView || !msg.frameInfo.isMainFrame) return;
	if (![msg.body isKindOfClass:[NSString class]] || ![(NSString *)msg.body isEqualToString:@"relogin"]) return;
	if (![self isDashboard:msg.frameInfo.request.URL]) return;
	lzwMessage("relogin");
}

@end

// ---------- C interface ----------

// onMain runs b on the main thread. Not the GCD main queue: its blocks do not nest, so
// lzw_finish would never run while a terminate: started from a queued block waits for it
// (NSTerminateLater). Run loop blocks in the common modes also run inside that wait.
static void onMain(void (^b)(void)) {
	if ([NSThread isMainThread]) {
		b();
		return;
	}
	CFRunLoopRef main = CFRunLoopGetMain();
	CFRunLoopPerformBlock(main, kCFRunLoopCommonModes, b);
	CFRunLoopWakeUp(main);
}

void lzw_run(const char *title, int width, int height, const char *loading, int hidden, const void *icon, int iconLen) {
	@autoreleasepool {
		[NSApplication sharedApplication];
		[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
		// Outside an .app bundle (go run, tests) the Dock would show a generic icon.
		if (icon && iconLen > 0 && [NSBundle mainBundle].bundleIdentifier == nil) {
			NSImage *img = [[NSImage alloc] initWithData:[NSData dataWithBytes:icon length:iconLen]];
			if (img) NSApp.applicationIconImage = img;
		}
		lz = [[LZController alloc] init];
		lz.hidden = hidden != 0;
		NSApp.delegate = lz;
		[lz buildMenus];
		[lz setupWindow:str(title) width:width height:height loading:str(loading)];
		if (!hidden) [NSApp activateIgnoringOtherApps:YES];
	}
	[NSApp run];
}

void lzw_load(const char *url, const char *origin) {
	NSString *u = str(url), *o = str(origin);
	onMain(^{
		NSURL *nu = [NSURL URLWithString:u];
		lz.origin = originOf([NSURL URLWithString:o]);
		if (!nu || !lz.origin) return;
		// Keep the status view (spinner) until the page finishes loading.
		if (!lz.pageShown) [lz setStatus:@"Abrindo o painel…" error:NO]; // also clears an error
		[lz.webView loadRequest:[NSURLRequest requestWithURL:nu]];
	});
}

void lzw_show(void) {
	onMain(^{
		[lz showWindow:nil];
	});
}

void lzw_eval(const char *js) {
	NSString *s = str(js);
	onMain(^{
		if (lz.pageShown) [lz.webView evaluateJavaScript:s completionHandler:nil];
	});
}

void lzw_status(const char *text, int isError) {
	NSString *s = str(text);
	onMain(^{
		[lz setStatus:s error:isError != 0];
	});
}

void lzw_terminate(void) {
	onMain(^{
		[NSApp terminate:nil];
	});
}

void lzw_finish(void) {
	onMain(^{
		lz.quitOK = YES;
		if (lz.quitPending) {
			[NSApp replyToApplicationShouldTerminate:YES];
		} else {
			[NSApp terminate:nil];
		}
	});
}
