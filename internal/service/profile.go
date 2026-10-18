package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // pictures may be uploaded as GIF (first frame)
	"image/jpeg"
	_ "image/png" // or PNG
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/media"
	"github.com/douglasdf99/litezap/internal/secure"
)

// Profile pictures are cached: the URL answer for pictureInfoTTL, the downloaded file for
// pictureFileTTL, and "no picture" for pictureMissTTL, so repeated reads neither reach
// WhatsApp nor spend the lookup quota.
const (
	pictureInfoTTL = 10 * time.Minute
	pictureFileTTL = time.Hour
	pictureMissTTL = 10 * time.Minute
	// maxPictureUpload bounds an uploaded profile picture before decoding.
	maxPictureUpload = 10 << 20
	maxPicturePixels = 40_000_000
	profilePicSide   = 640
)

type pictureEntry struct {
	info    *workerv1.GetProfilePictureResponse // nil: no picture (missing or hidden)
	missMsg string
	path    string // downloaded file ("" until downloaded)
	at      time.Time
	fileAt  time.Time
}

type pictureCache struct {
	mu      sync.Mutex
	entries map[string]*pictureEntry // instance \x00 jid \x00 preview
}

func (c *pictureCache) get(k string) (pictureEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok {
		return pictureEntry{}, false
	}
	return *e, true
}

func (c *pictureCache) put(k string, e pictureEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*pictureEntry{}
	}
	if len(c.entries) > 5000 { // bounded: drop everything rather than track an LRU
		c.entries = map[string]*pictureEntry{}
	}
	c.entries[k] = &e
}

func (c *pictureCache) forget(instanceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if strings.HasPrefix(k, instanceID+"\x00") {
			delete(c.entries, k)
		}
	}
}

// PictureView is a profile picture: WhatsApp's CDN URL (it expires after a while; use
// the download route to keep a copy) and its id (changes with the picture).
type PictureView struct {
	JID  string `json:"jid"`
	ID   string `json:"id"`
	URL  string `json:"url"`
	Type string `json:"type"` // image | preview
}

// lookupGuard applies the operator suspension and the lookup quota to a network lookup
// about a third party.
func (a *Account) lookupGuard(instanceID string) error {
	if a.Sending != nil {
		if err := a.Sending.Check(instanceID); err != nil {
			return err
		}
	}
	if !a.lookupQuota.take(instanceID, time.Now(), lookupsPerMinute, lookupsPerDay) {
		return apierr.New(http.StatusTooManyRequests, "rate_limited",
			"too many contact lookups; at most 30 per minute and 1000 per day per instance", true)
	}
	return nil
}

// contactJID normalizes a contact (phone or user JID); groups too when allowGroup.
func contactJID(raw string, allowGroup bool) (string, error) {
	jid, err := ChatJID(raw)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(jid, "@g.us") && !allowGroup {
		return "", apierr.Validation("a contact is required (not a group)")
	}
	return jid, nil
}

func (a *Account) instanceClient(ctx context.Context, instanceID string) (workerv1.WorkerClient, error) {
	if _, err := a.st().GetInstance(ctx, instanceID); err != nil {
		return nil, mapErr(err)
	}
	return a.Messaging.client(instanceID)
}

func pictureKey(instanceID, jid string, preview bool) string {
	k := instanceID + "\x00" + jid + "\x00"
	if preview {
		k += "p"
	}
	return k
}

// pictureInfo answers from the cache or asks WhatsApp (spending the quota, except for the
// account's own picture: jid "").
func (a *Account) pictureInfo(ctx context.Context, instanceID, jid string, preview bool) (pictureEntry, error) {
	k := pictureKey(instanceID, jid, preview)
	if e, ok := a.pictures.get(k); ok {
		ttl := pictureInfoTTL
		if e.info == nil {
			ttl = pictureMissTTL
		}
		if time.Since(e.at) < ttl {
			return e, nil
		}
	}
	cli, err := a.instanceClient(ctx, instanceID)
	if err != nil {
		return pictureEntry{}, err
	}
	if jid != "" {
		if err := a.lookupGuard(instanceID); err != nil {
			return pictureEntry{}, err
		}
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	info, err := cli.GetProfilePicture(rctx, &workerv1.GetProfilePictureRequest{Jid: jid, Preview: preview})
	if err != nil {
		mapped := accountErr(err)
		var e *apierr.Error
		if errors.As(mapped, &e) && e.Code == "not_found" {
			a.pictures.put(k, pictureEntry{missMsg: e.Message, at: time.Now()})
		}
		return pictureEntry{}, mapped
	}
	e := pictureEntry{info: info, at: time.Now()}
	if old, ok := a.pictures.get(k); ok && old.info != nil && old.info.Id == info.Id {
		e.path, e.fileAt = old.path, old.fileAt // same picture: the file is still good
	}
	a.pictures.put(k, e)
	return e, nil
}

func missing(e pictureEntry) error {
	msg := e.missMsg
	if msg == "" {
		msg = "no profile picture"
	}
	return apierr.New(http.StatusNotFound, "not_found", msg, false)
}

// ContactPicture returns a contact's (or group's) profile picture URL. No picture, or one
// hidden by the contact's privacy settings, is 404 not_found.
func (a *Account) ContactPicture(ctx context.Context, instanceID, raw string, preview bool) (PictureView, error) {
	jid, err := contactJID(raw, true)
	if err != nil {
		return PictureView{}, err
	}
	e, err := a.pictureInfo(ctx, instanceID, jid, preview)
	if err != nil {
		return PictureView{}, err
	}
	if e.info == nil {
		return PictureView{}, missing(e)
	}
	return PictureView{JID: jid, ID: e.info.Id, URL: e.info.Url, Type: e.info.Type}, nil
}

// ContactPictureFile downloads a contact's profile picture through the worker (and so
// through the instance proxy) and returns the cached JPEG.
func (a *Account) ContactPictureFile(ctx context.Context, instanceID, raw string, preview bool) (MediaFile, error) {
	jid, err := contactJID(raw, true)
	if err != nil {
		return MediaFile{}, err
	}
	return a.pictureFile(ctx, instanceID, jid, preview)
}

// ProfilePictureFile downloads the account's own picture (no lookup quota).
func (a *Account) ProfilePictureFile(ctx context.Context, instanceID string, preview bool) (MediaFile, error) {
	return a.pictureFile(ctx, instanceID, "", preview)
}

func (a *Account) pictureFile(ctx context.Context, instanceID, jid string, preview bool) (MediaFile, error) {
	k := pictureKey(instanceID, jid, preview)
	if e, ok := a.pictures.get(k); ok && e.path != "" && time.Since(e.fileAt) < pictureFileTTL {
		if _, err := os.Stat(e.path); err == nil {
			return MediaFile{Path: e.path, Mime: "image/jpeg", FileName: "picture.jpg"}, nil
		}
	}
	e, err := a.pictureInfo(ctx, instanceID, jid, preview)
	if err != nil {
		return MediaFile{}, err
	}
	if e.info == nil {
		return MediaFile{}, missing(e)
	}
	cli, err := a.instanceClient(ctx, instanceID)
	if err != nil {
		return MediaFile{}, err
	}
	dir := filepath.Join(a.Messaging.DataDir, "media", instanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return MediaFile{}, err
	}
	sum := sha256.Sum256([]byte(k))
	final := filepath.Join(dir, "avatar-"+hex.EncodeToString(sum[:8])+".jpg")
	tmp, err := os.CreateTemp(dir, "avatar-*.part")
	if err != nil {
		return MediaFile{}, err
	}
	part := tmp.Name()
	tmp.Close()
	dctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	if _, err := cli.DownloadProfilePicture(dctx, &workerv1.DownloadProfilePictureRequest{Jid: jid, Preview: preview, DestPath: part}); err != nil {
		os.Remove(part)
		return MediaFile{}, accountErr(err)
	}
	if err := os.Rename(part, final); err != nil {
		os.Remove(part)
		return MediaFile{}, err
	}
	e.path, e.fileAt = final, time.Now()
	a.pictures.put(k, e)
	pruneAvatars(dir, final)
	return MediaFile{Path: final, Mime: "image/jpeg", FileName: "picture.jpg"}, nil
}

// pruneAvatars removes downloaded pictures older than a day (keep is the fresh one).
func pruneAvatars(dir, keep string) {
	list, _ := filepath.Glob(filepath.Join(dir, "avatar-*"))
	for _, p := range list {
		if p == keep {
			continue
		}
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) > 24*time.Hour {
			os.Remove(p)
		}
	}
}

// ContactProfileView is what WhatsApp tells about a contact on demand.
type ContactProfileView struct {
	JID          string               `json:"jid"`
	LID          string               `json:"lid,omitempty"`
	About        string               `json:"about,omitempty"`
	PictureID    string               `json:"picture_id,omitempty"`
	VerifiedName string               `json:"verified_name,omitempty"` // business accounts
	Business     *BusinessProfileView `json:"business,omitempty"`
}

type BusinessProfileView struct {
	Address    string   `json:"address,omitempty"`
	Email      string   `json:"email,omitempty"`
	Categories []string `json:"categories,omitempty"`
}

// ContactProfile asks WhatsApp for a contact's about text, picture id, verified business
// name and (business accounts) business profile. Spends the lookup quota.
func (a *Account) ContactProfile(ctx context.Context, instanceID, raw string) (ContactProfileView, error) {
	jid, err := contactJID(raw, false)
	if err != nil {
		return ContactProfileView{}, err
	}
	cli, err := a.instanceClient(ctx, instanceID)
	if err != nil {
		return ContactProfileView{}, err
	}
	if err := a.lookupGuard(instanceID); err != nil {
		return ContactProfileView{}, err
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	u, err := cli.GetUserInfo(rctx, &workerv1.GetUserInfoRequest{Jid: jid, Business: true})
	if err != nil {
		return ContactProfileView{}, accountErr(err)
	}
	v := ContactProfileView{JID: u.Jid, LID: u.Lid, About: u.About, PictureID: u.PictureId, VerifiedName: u.VerifiedName}
	if v.JID == "" {
		v.JID = jid
	}
	if b := u.Business; b != nil {
		v.Business = &BusinessProfileView{Address: b.Address, Email: b.Email, Categories: b.Categories}
	}
	return v, nil
}

// Blocklist lists the blocked contacts (phone-number JIDs when known).
func (a *Account) Blocklist(ctx context.Context, instanceID string) ([]string, error) {
	cli, err := a.instanceClient(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	resp, err := cli.GetBlocklist(rctx, &workerv1.GetBlocklistRequest{})
	if err != nil {
		return nil, accountErr(err)
	}
	return nonNil(resp.Jids), nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// SetBlocked blocks or unblocks a contact and returns the new blocklist.
func (a *Account) SetBlocked(ctx context.Context, instanceID, raw string, block bool) ([]string, error) {
	jid, err := contactJID(raw, false)
	if err != nil {
		return nil, err
	}
	cli, err := a.instanceClient(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	resp, err := cli.UpdateBlocklist(rctx, &workerv1.UpdateBlocklistRequest{Jid: jid, Block: block})
	if err != nil {
		return nil, accountErr(err)
	}
	return nonNil(resp.Jids), nil
}

// ProfileView is the account's own profile (INS-8/INS-10).
type ProfileView struct {
	JID          string       `json:"jid"`
	LID          string       `json:"lid,omitempty"`
	Name         string       `json:"name,omitempty"` // push name shown to contacts
	About        string       `json:"about,omitempty"`
	BusinessName string       `json:"business_name,omitempty"`
	Platform     string       `json:"platform,omitempty"` // of the paired phone, as WhatsApp reports it
	Picture      *PictureView `json:"picture,omitempty"`
}

// Profile returns the account profile; the picture is looked up without spending the
// contact lookup quota (it is the account's own).
func (a *Account) Profile(ctx context.Context, instanceID string) (ProfileView, error) {
	cli, err := a.instanceClient(ctx, instanceID)
	if err != nil {
		return ProfileView{}, err
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	p, err := cli.GetProfile(rctx, &workerv1.GetProfileRequest{})
	if err != nil {
		return ProfileView{}, accountErr(err)
	}
	v := ProfileView{JID: p.Jid, LID: p.Lid, Name: p.PushName, About: p.About, BusinessName: p.BusinessName, Platform: p.Platform}
	if p.PictureId != "" {
		if pic, err := cli.GetProfilePicture(rctx, &workerv1.GetProfilePictureRequest{}); err == nil {
			v.Picture = &PictureView{JID: p.Jid, ID: pic.Id, URL: pic.Url, Type: pic.Type}
		}
	}
	return v, nil
}

// ProfilePatch changes the account's name and/or about text.
type ProfilePatch struct {
	Name  *string `json:"name,omitempty"`
	About *string `json:"about,omitempty"`
}

// UpdateProfile sets the push name (app state) and/or the about text.
func (a *Account) UpdateProfile(ctx context.Context, instanceID string, p ProfilePatch) (ProfileView, error) {
	if p.Name == nil && p.About == nil {
		return ProfileView{}, apierr.Validation("nothing to change: set name or about")
	}
	req := &workerv1.UpdateProfileRequest{}
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		if name == "" || len([]rune(name)) > 25 {
			return ProfileView{}, apierr.Validation("name must have 1 to 25 characters")
		}
		req.SetName, req.Name = true, name
	}
	if p.About != nil {
		if len([]rune(*p.About)) > 139 {
			return ProfileView{}, apierr.Validation("about is limited to 139 characters")
		}
		req.SetAbout, req.About = true, *p.About
	}
	if err := a.updateProfile(ctx, instanceID, req); err != nil {
		return ProfileView{}, err
	}
	return a.Profile(ctx, instanceID)
}

func (a *Account) updateProfile(ctx context.Context, instanceID string, req *workerv1.UpdateProfileRequest) error {
	cli, err := a.instanceClient(ctx, instanceID)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	if _, err := cli.UpdateProfile(rctx, req); err != nil {
		return accountErr(err)
	}
	a.pictures.forget(instanceID) // our own picture may have changed
	return nil
}

// PictureInput is an image given by URL or base64 (JPEG, PNG or GIF).
type PictureInput struct {
	URL    string `json:"url,omitempty"`
	Base64 string `json:"base64,omitempty"`
}

// SetProfilePicture sets the account picture: the image is cropped to a centered square
// and resized to 640x640 JPEG, as WhatsApp's apps do.
func (a *Account) SetProfilePicture(ctx context.Context, instanceID string, in PictureInput) (ProfileView, error) {
	if (in.URL == "") == (in.Base64 == "") {
		return ProfileView{}, apierr.Validation("picture needs exactly one of url or base64")
	}
	if _, err := a.instanceClient(ctx, instanceID); err != nil {
		return ProfileView{}, err
	}
	release, err := a.Messaging.acquireMedia(ctx)
	if err != nil {
		return ProfileView{}, err
	}
	defer release()
	var data []byte
	if in.URL != "" {
		data, _, err = a.Messaging.Fetcher.Fetch(ctx, in.URL, maxPictureUpload)
	} else {
		data, _, err = media.DecodeBase64(in.Base64, maxPictureUpload)
	}
	switch {
	case errors.Is(err, media.ErrTooLarge):
		return ProfileView{}, apierr.New(http.StatusRequestEntityTooLarge, "media_too_large", "picture exceeds 10MB", false)
	case errors.Is(err, media.ErrBlocked):
		return ProfileView{}, apierr.Validation("url points to a private or local address")
	case errors.Is(err, media.ErrInvalidURL):
		return ProfileView{}, apierr.Validation(err.Error())
	case errors.Is(err, media.ErrFetch):
		return ProfileView{}, apierr.New(http.StatusUnprocessableEntity, "media_fetch_failed", "url could not be downloaded", true)
	case err != nil:
		return ProfileView{}, apierr.Validation(err.Error())
	}
	jpg, err := squareJPEG(data, profilePicSide)
	if err != nil {
		return ProfileView{}, apierr.Validation("picture: " + err.Error())
	}
	dir := filepath.Join(a.Messaging.DataDir, "outbox", instanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ProfileView{}, err
	}
	path := filepath.Join(dir, "profile-"+secure.NewID("")+".jpg")
	if err := os.WriteFile(path, jpg, 0o600); err != nil {
		return ProfileView{}, err
	}
	defer os.Remove(path)
	if err := a.updateProfile(ctx, instanceID, &workerv1.UpdateProfileRequest{PicturePath: path}); err != nil {
		return ProfileView{}, err
	}
	return a.Profile(ctx, instanceID)
}

// RemoveProfilePicture removes the account picture.
func (a *Account) RemoveProfilePicture(ctx context.Context, instanceID string) error {
	return a.updateProfile(ctx, instanceID, &workerv1.UpdateProfileRequest{RemovePicture: true})
}

// squareJPEG decodes a JPEG, PNG or GIF, crops the centered square and scales it to side x
// side (area average when shrinking) as a JPEG.
func squareJPEG(data []byte, side int) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png" && format != "gif") {
		return nil, errors.New("must be a JPEG, PNG or GIF image (WebP is not supported)")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width*cfg.Height > maxPicturePixels {
		return nil, errors.New("picture dimensions are out of range")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("picture could not be decoded")
	}
	b := src.Bounds()
	n := min(b.Dx(), b.Dy())
	x0, y0 := b.Min.X+(b.Dx()-n)/2, b.Min.Y+(b.Dy()-n)/2
	sq := image.NewRGBA(image.Rect(0, 0, n, n))
	draw.Draw(sq, sq.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src) // transparent PNG -> white
	draw.Draw(sq, sq.Bounds(), src, image.Point{X: x0, Y: y0}, draw.Over)
	out := sq
	if n != side {
		out = image.NewRGBA(image.Rect(0, 0, side, side))
		for y := 0; y < side; y++ {
			sy0, sy1 := y*n/side, max((y+1)*n/side, y*n/side+1)
			for x := 0; x < side; x++ {
				sx0, sx1 := x*n/side, max((x+1)*n/side, x*n/side+1)
				var r, g, bl, cnt uint32
				for yy := sy0; yy < sy1; yy++ {
					off := yy*sq.Stride + sx0*4
					for xx := sx0; xx < sx1; xx++ {
						r += uint32(sq.Pix[off])
						g += uint32(sq.Pix[off+1])
						bl += uint32(sq.Pix[off+2])
						off += 4
						cnt++
					}
				}
				o := y*out.Stride + x*4
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8(r/cnt), uint8(g/cnt), uint8(bl/cnt), 255
			}
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Privacy setting names: the API's names and whatsmeow's.
var privacyAPINames = map[string]string{
	"last_seen": "last", "online": "online", "profile_photo": "profile", "about": "status",
	"groups_add": "groupadd", "read_receipts": "readreceipts", "calls": "calladd", "messages": "messages",
}

// PrivacyView maps each privacy setting (last_seen, online, profile_photo, about,
// groups_add, read_receipts, calls, messages) to its value.
type PrivacyView map[string]string

func privacyView(m map[string]string) PrivacyView {
	out := PrivacyView{}
	for api, wa := range privacyAPINames {
		if v, ok := m[wa]; ok {
			out[api] = v
		}
	}
	return out
}

// Privacy reads the account's privacy settings from WhatsApp.
func (a *Account) Privacy(ctx context.Context, instanceID string) (PrivacyView, error) {
	cli, err := a.instanceClient(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	p, err := cli.GetPrivacy(rctx, &workerv1.GetPrivacyRequest{})
	if err != nil {
		return nil, accountErr(err)
	}
	return privacyView(p.Settings), nil
}

// PrivacyPatch changes privacy settings (API names) and/or the default disappearing
// timer for new chats (0, 86400, 604800 or 7776000 seconds).
type PrivacyPatch struct {
	Settings                   map[string]string
	DefaultDisappearingSeconds *int
}

// UpdatePrivacy applies each setting in turn (a failure stops there) and returns the
// settings as WhatsApp has them afterwards.
func (a *Account) UpdatePrivacy(ctx context.Context, instanceID string, p PrivacyPatch) (PrivacyView, error) {
	if len(p.Settings) == 0 && p.DefaultDisappearingSeconds == nil {
		return nil, apierr.Validation("nothing to change")
	}
	for name, v := range p.Settings {
		wa, ok := privacyAPINames[name]
		if !ok {
			return nil, apierr.Validation("unknown privacy setting " + name)
		}
		if !validPrivacy(wa, v) {
			return nil, apierr.Validation("invalid value for " + name)
		}
	}
	if d := p.DefaultDisappearingSeconds; d != nil {
		switch *d {
		case 0, 86400, 604800, 7776000:
		default:
			return nil, apierr.Validation("default_disappearing_seconds must be 0, 86400, 604800 or 7776000")
		}
	}
	cli, err := a.instanceClient(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	var last *workerv1.Privacy
	if d := p.DefaultDisappearingSeconds; d != nil {
		if last, err = cli.SetPrivacy(rctx, &workerv1.SetPrivacyRequest{SetDefaultDisappearing: true, DefaultDisappearingSeconds: uint32(*d)}); err != nil {
			return nil, accountErr(err)
		}
	}
	for name, v := range p.Settings {
		if last, err = cli.SetPrivacy(rctx, &workerv1.SetPrivacyRequest{Name: privacyAPINames[name], Value: v}); err != nil {
			return nil, accountErr(err)
		}
	}
	return privacyView(last.GetSettings()), nil
}

// validPrivacy mirrors the values whatsmeow documents per setting (types/user.go).
func validPrivacy(name, value string) bool {
	four := []string{"all", "contacts", "contact_blacklist", "none"}
	allowed := map[string][]string{
		"last": four, "profile": four, "status": four, "groupadd": four,
		"readreceipts": {"all", "none"}, "online": {"all", "match_last_seen"},
		"calladd": {"all", "known"}, "messages": {"all", "contacts"},
	}
	for _, v := range allowed[name] {
		if v == value {
			return true
		}
	}
	return false
}
