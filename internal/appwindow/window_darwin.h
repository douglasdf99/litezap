// C interface of window_darwin.m (see window_darwin.go).
void lzw_run(const char *title, int width, int height, const char *loading, int hidden, const void *icon, int iconLen);
void lzw_load(const char *url, const char *origin);
void lzw_show(void);
void lzw_eval(const char *js);
void lzw_status(const char *text, int isError);
void lzw_terminate(void);
void lzw_finish(void);
