package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

const (
	defaultTimeoutMs         int32 = 15000
	defaultTopSongCount      int32 = 10
	defaultSimilarArtistCount       = 10

	sourceNetEase     = "netease"
	sourceQQ          = "qq"
	sourceMusicBrainz = "musicbrainz"

	fieldArtistURL       = "artist_url"
	fieldArtistBiography = "artist_biography"
	fieldSimilarArtists  = "similar_artists"
	fieldArtistImages    = "artist_images"
	fieldArtistTopSongs  = "artist_top_songs"
	fieldAlbumInfo       = "album_info"
	fieldAlbumImages     = "album_images"

	loadBalanceRandom     = "random"
	loadBalanceRoundRobin = "roundrobin"

	defaultUserAgent = "sumeta/0.1 (+https://github.com/kennysoul/sumusic)"
)

var (
	errNotFound      = errors.New("not found")
	errEmptyResponse = errors.New("empty response")
)

type pluginConfig struct {
	sourceOrder         []string
	enableFallback      bool
	enabledFields       map[string]bool
	timeoutMs           int32
	neteaseBaseURLs     []string
	neteaseLoadBalancing string
}

type sumetaPlugin struct {
	cfg     pluginConfig
	netease *neteaseAdapter
	qq      *qqAdapter
	mb      *musicBrainzAdapter
}

func init() {
	metadata.Register(newSumetaPlugin())
}

var (
	_ metadata.ArtistURLProvider       = (*sumetaPlugin)(nil)
	_ metadata.ArtistBiographyProvider = (*sumetaPlugin)(nil)
	_ metadata.SimilarArtistsProvider  = (*sumetaPlugin)(nil)
	_ metadata.ArtistImagesProvider    = (*sumetaPlugin)(nil)
	_ metadata.ArtistTopSongsProvider  = (*sumetaPlugin)(nil)
	_ metadata.AlbumInfoProvider       = (*sumetaPlugin)(nil)
	_ metadata.AlbumImagesProvider     = (*sumetaPlugin)(nil)
)

func newSumetaPlugin() *sumetaPlugin {
	cfg := loadConfig()
	return &sumetaPlugin{
		cfg:     cfg,
		netease: newNetEaseAdapter(cfg),
		qq:      newQQAdapter(cfg),
		mb:      newMusicBrainzAdapter(cfg),
	}
}

func (p *sumetaPlugin) GetArtistURL(req metadata.ArtistRequest) (*metadata.ArtistURLResponse, error) {
	if !p.fieldEnabled(fieldArtistURL) {
		return nil, nil
	}
	var out *metadata.ArtistURLResponse
	p.walkSources(func(source string) (bool, error) {
		resp, err := p.getArtistURLFromSource(source, req)
		if err != nil {
			return false, err
		}
		if resp != nil && strings.TrimSpace(resp.URL) != "" {
			out = resp
			return true, nil
		}
		return false, nil
	})
	return out, nil
}

func (p *sumetaPlugin) GetArtistBiography(req metadata.ArtistRequest) (*metadata.ArtistBiographyResponse, error) {
	if !p.fieldEnabled(fieldArtistBiography) {
		return nil, nil
	}
	var out *metadata.ArtistBiographyResponse
	p.walkSources(func(source string) (bool, error) {
		resp, err := p.getArtistBiographyFromSource(source, req)
		if err != nil {
			return false, err
		}
		if resp != nil && strings.TrimSpace(resp.Biography) != "" {
			out = resp
			return true, nil
		}
		return false, nil
	})
	return out, nil
}

func (p *sumetaPlugin) GetSimilarArtists(req metadata.SimilarArtistsRequest) (*metadata.SimilarArtistsResponse, error) {
	if !p.fieldEnabled(fieldSimilarArtists) {
		return nil, nil
	}
	if req.Limit <= 0 {
		req.Limit = defaultSimilarArtistCount
	}
	var out *metadata.SimilarArtistsResponse
	p.walkSources(func(source string) (bool, error) {
		resp, err := p.getSimilarArtistsFromSource(source, req)
		if err != nil {
			return false, err
		}
		if resp != nil && len(resp.Artists) > 0 {
			out = resp
			return true, nil
		}
		return false, nil
	})
	return out, nil
}

func (p *sumetaPlugin) GetArtistImages(req metadata.ArtistRequest) (*metadata.ArtistImagesResponse, error) {
	if !p.fieldEnabled(fieldArtistImages) {
		return nil, nil
	}
	var out *metadata.ArtistImagesResponse
	p.walkSources(func(source string) (bool, error) {
		resp, err := p.getArtistImagesFromSource(source, req)
		if err != nil {
			return false, err
		}
		if resp != nil && len(resp.Images) > 0 {
			out = resp
			return true, nil
		}
		return false, nil
	})
	return out, nil
}

func (p *sumetaPlugin) GetArtistTopSongs(req metadata.TopSongsRequest) (*metadata.TopSongsResponse, error) {
	if !p.fieldEnabled(fieldArtistTopSongs) {
		return nil, nil
	}
	if req.Count <= 0 {
		req.Count = defaultTopSongCount
	}
	var out *metadata.TopSongsResponse
	p.walkSources(func(source string) (bool, error) {
		resp, err := p.getArtistTopSongsFromSource(source, req)
		if err != nil {
			return false, err
		}
		if resp != nil && len(resp.Songs) > 0 {
			out = resp
			return true, nil
		}
		return false, nil
	})
	return out, nil
}

func (p *sumetaPlugin) GetAlbumInfo(req metadata.AlbumRequest) (*metadata.AlbumInfoResponse, error) {
	if !p.fieldEnabled(fieldAlbumInfo) {
		return nil, nil
	}
	var out *metadata.AlbumInfoResponse
	p.walkSources(func(source string) (bool, error) {
		resp, err := p.getAlbumInfoFromSource(source, req)
		if err != nil {
			return false, err
		}
		if resp != nil && (strings.TrimSpace(resp.Description) != "" || strings.TrimSpace(resp.URL) != "" || strings.TrimSpace(resp.MBID) != "" || strings.TrimSpace(resp.Name) != "") {
			out = resp
			return true, nil
		}
		return false, nil
	})
	return out, nil
}

func (p *sumetaPlugin) GetAlbumImages(req metadata.AlbumRequest) (*metadata.AlbumImagesResponse, error) {
	if !p.fieldEnabled(fieldAlbumImages) {
		return nil, nil
	}
	var out *metadata.AlbumImagesResponse
	p.walkSources(func(source string) (bool, error) {
		resp, err := p.getAlbumImagesFromSource(source, req)
		if err != nil {
			return false, err
		}
		if resp != nil && len(resp.Images) > 0 {
			out = resp
			return true, nil
		}
		return false, nil
	})
	return out, nil
}

func (p *sumetaPlugin) fieldEnabled(field string) bool {
	enabled, ok := p.cfg.enabledFields[field]
	if !ok {
		return true
	}
	return enabled
}

func (p *sumetaPlugin) walkSources(run func(source string) (bool, error)) bool {
	if len(p.cfg.sourceOrder) == 0 {
		return false
	}
	attempts := len(p.cfg.sourceOrder)
	if !p.cfg.enableFallback && attempts > 1 {
		attempts = 1
	}
	for i := 0; i < attempts; i++ {
		source := p.cfg.sourceOrder[i]
		ok, err := run(source)
		if err != nil {
			pdk.Log(pdk.LogDebug, "sumeta: source "+source+" failed: "+err.Error())
		}
		if ok {
			return true
		}
	}
	return false
}

func (p *sumetaPlugin) getArtistURLFromSource(source string, req metadata.ArtistRequest) (*metadata.ArtistURLResponse, error) {
	switch source {
	case sourceNetEase:
		return p.netease.GetArtistURL(req)
	case sourceQQ:
		return p.qq.GetArtistURL(req)
	case sourceMusicBrainz:
		return p.mb.GetArtistURL(req)
	default:
		return nil, nil
	}
}

func (p *sumetaPlugin) getArtistBiographyFromSource(source string, req metadata.ArtistRequest) (*metadata.ArtistBiographyResponse, error) {
	switch source {
	case sourceNetEase:
		return p.netease.GetArtistBiography(req)
	case sourceQQ:
		return p.qq.GetArtistBiography(req)
	case sourceMusicBrainz:
		return p.mb.GetArtistBiography(req)
	default:
		return nil, nil
	}
}

func (p *sumetaPlugin) getArtistImagesFromSource(source string, req metadata.ArtistRequest) (*metadata.ArtistImagesResponse, error) {
	switch source {
	case sourceNetEase:
		return p.netease.GetArtistImages(req)
	case sourceQQ:
		return p.qq.GetArtistImages(req)
	case sourceMusicBrainz:
		return p.mb.GetArtistImages(req)
	default:
		return nil, nil
	}
}

func (p *sumetaPlugin) getSimilarArtistsFromSource(source string, req metadata.SimilarArtistsRequest) (*metadata.SimilarArtistsResponse, error) {
	switch source {
	case sourceNetEase:
		return p.netease.GetSimilarArtists(req)
	case sourceQQ:
		return p.qq.GetSimilarArtists(req)
	case sourceMusicBrainz:
		return p.mb.GetSimilarArtists(req)
	default:
		return nil, nil
	}
}

func (p *sumetaPlugin) getArtistTopSongsFromSource(source string, req metadata.TopSongsRequest) (*metadata.TopSongsResponse, error) {
	switch source {
	case sourceNetEase:
		return p.netease.GetArtistTopSongs(req)
	case sourceQQ:
		return p.qq.GetArtistTopSongs(req)
	case sourceMusicBrainz:
		return p.mb.GetArtistTopSongs(req)
	default:
		return nil, nil
	}
}

func (p *sumetaPlugin) getAlbumInfoFromSource(source string, req metadata.AlbumRequest) (*metadata.AlbumInfoResponse, error) {
	switch source {
	case sourceNetEase:
		return p.netease.GetAlbumInfo(req)
	case sourceQQ:
		return p.qq.GetAlbumInfo(req)
	case sourceMusicBrainz:
		return p.mb.GetAlbumInfo(req)
	default:
		return nil, nil
	}
}

func (p *sumetaPlugin) getAlbumImagesFromSource(source string, req metadata.AlbumRequest) (*metadata.AlbumImagesResponse, error) {
	switch source {
	case sourceNetEase:
		return p.netease.GetAlbumImages(req)
	case sourceQQ:
		return p.qq.GetAlbumImages(req)
	case sourceMusicBrainz:
		return p.mb.GetAlbumImages(req)
	default:
		return nil, nil
	}
}

func loadConfig() pluginConfig {
	cfg := pluginConfig{
		sourceOrder:         []string{sourceNetEase, sourceQQ, sourceMusicBrainz},
		enableFallback:      true,
		enabledFields:       defaultEnabledFields(),
		timeoutMs:           defaultTimeoutMs,
		neteaseBaseURLs:     defaultNetEaseAPIBaseURLs(),
		neteaseLoadBalancing: loadBalanceRandom,
	}

	if raw, ok := getFirstConfig("SourceOrder", "Sources", "source_order", "sources"); ok {
		parsed := parseSourceOrder(raw)
		if len(parsed) > 0 {
			cfg.sourceOrder = parsed
		}
	}

	if raw, ok := getFirstConfig("EnableFallback", "Fallback", "enable_fallback", "fallback"); ok {
		cfg.enableFallback = parseBool(raw, cfg.enableFallback)
	}

	if raw, ok := getFirstConfig("TimeoutMs", "HTTPTimeoutMs", "timeout_ms", "http_timeout_ms"); ok {
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && parsed > 0 {
			cfg.timeoutMs = int32(parsed)
		}
	}

	if raw, ok := getFirstConfig("EnabledFields", "FetchFields", "OnlyFields", "enabled_fields", "fetch_fields", "only_fields"); ok && strings.TrimSpace(raw) != "" {
		cfg.enabledFields = parseEnabledFieldSet(raw)
	}
	if raw, ok := getFirstConfig("DisabledFields", "DisableFields", "disabled_fields", "disable_fields"); ok && strings.TrimSpace(raw) != "" {
		for _, name := range parseFieldList(raw) {
			cfg.enabledFields[name] = false
		}
	}

	applyFieldToggle := func(field string, configKeys ...string) {
		if raw, ok := getFirstConfig(configKeys...); ok {
			cfg.enabledFields[field] = parseBool(raw, cfg.enabledFields[field])
		}
	}
	applyFieldToggle(fieldArtistURL, "EnableArtistURL", "enable_artist_url")
	applyFieldToggle(fieldArtistBiography, "EnableArtistBiography", "enable_artist_biography")
	applyFieldToggle(fieldSimilarArtists, "EnableSimilarArtists", "enable_similar_artists")
	applyFieldToggle(fieldArtistImages, "EnableArtistImages", "enable_artist_images")
	applyFieldToggle(fieldArtistTopSongs, "EnableArtistTopSongs", "enable_artist_top_songs")
	applyFieldToggle(fieldAlbumInfo, "EnableAlbumInfo", "enable_album_info")
	applyFieldToggle(fieldAlbumImages, "EnableAlbumImages", "enable_album_images")

	sourceEnabled := map[string]bool{
		sourceNetEase:     true,
		sourceQQ:          true,
		sourceMusicBrainz: true,
	}
	applySourceToggle := func(source string, configKeys ...string) {
		if raw, ok := getFirstConfig(configKeys...); ok {
			sourceEnabled[source] = parseBool(raw, sourceEnabled[source])
		}
	}
	applySourceToggle(sourceNetEase, "EnableNetEase", "enable_netease")
	applySourceToggle(sourceQQ, "EnableQQ", "enable_qq")
	applySourceToggle(sourceMusicBrainz, "EnableMusicBrainz", "enable_musicbrainz")

	cfg.sourceOrder = filterEnabledSources(cfg.sourceOrder, sourceEnabled)
	if len(cfg.sourceOrder) == 0 {
		cfg.sourceOrder = []string{sourceNetEase, sourceQQ, sourceMusicBrainz}
	}

	if raw, ok := getFirstConfig("APIUrls", "NetEaseAPIUrls", "api_urls", "netease_api_urls"); ok {
		parsed := parseBaseURLs(raw)
		if len(parsed) > 0 {
			cfg.neteaseBaseURLs = parsed
		}
	}
	if raw, ok := getFirstConfig("LoadBalanceMode", "NetEaseLoadBalanceMode", "load_balance_mode", "netease_load_balance_mode"); ok {
		mode := strings.ToLower(strings.TrimSpace(raw))
		if mode == loadBalanceRandom || mode == loadBalanceRoundRobin {
			cfg.neteaseLoadBalancing = mode
		}
	}

	pdk.Log(pdk.LogInfo, "sumeta: sources="+strings.Join(cfg.sourceOrder, ",")+", fallback="+strconv.FormatBool(cfg.enableFallback))
	return cfg
}

func getFirstConfig(keys ...string) (string, bool) {
	for _, key := range keys {
		if v, ok := pdk.GetConfig(key); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

func parseBool(v string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on", "enable", "enabled":
		return true
	case "0", "false", "no", "n", "off", "disable", "disabled":
		return false
	default:
		return fallback
	}
}

func parseSourceOrder(raw string) []string {
	parts := splitList(raw)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		n := normalizeSourceName(p)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func normalizeSourceName(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "netease", "wangyi", "163", "netease_music":
		return sourceNetEase
	case "qq", "qqmusic", "qq_music":
		return sourceQQ
	case "musicbrainz", "mb", "mbz":
		return sourceMusicBrainz
	default:
		return ""
	}
}

func filterEnabledSources(order []string, enabled map[string]bool) []string {
	out := make([]string, 0, len(order))
	for _, src := range order {
		if enabled[src] {
			out = append(out, src)
		}
	}
	return out
}

func defaultEnabledFields() map[string]bool {
	return map[string]bool{
		fieldArtistURL:       true,
		fieldArtistBiography: true,
		fieldSimilarArtists:  true,
		fieldArtistImages:    true,
		fieldArtistTopSongs:  true,
		fieldAlbumInfo:       true,
		fieldAlbumImages:     true,
	}
}

func parseEnabledFieldSet(raw string) map[string]bool {
	result := map[string]bool{
		fieldArtistURL:       false,
		fieldArtistBiography: false,
		fieldSimilarArtists:  false,
		fieldArtistImages:    false,
		fieldArtistTopSongs:  false,
		fieldAlbumInfo:       false,
		fieldAlbumImages:     false,
	}
	for _, name := range parseFieldList(raw) {
		result[name] = true
	}
	return result
}

func parseFieldList(raw string) []string {
	parts := splitList(raw)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		n := normalizeFieldName(p)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func normalizeFieldName(v string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, ".", "_")
	s = strings.ReplaceAll(s, ":", "_")
	s = strings.ReplaceAll(s, "__", "_")

	switch s {
	case "artist_url", "url", "artisturl":
		return fieldArtistURL
	case "artist_biography", "artist_bio", "biography", "bio", "artistbiography":
		return fieldArtistBiography
	case "similar_artists", "similarartists", "artist_similar", "artist_similars", "similar":
		return fieldSimilarArtists
	case "artist_images", "artist_image", "images", "artistimages":
		return fieldArtistImages
	case "artist_top_songs", "artist_topsongs", "topsongs", "top_songs", "artisttopsongs":
		return fieldArtistTopSongs
	case "album_info", "albuminfo", "album_description":
		return fieldAlbumInfo
	case "album_images", "album_image", "albumimages":
		return fieldAlbumImages
	default:
		return ""
	}
}

func splitList(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', '|', ' ', '\n', '\r', '\t':
			return true
		default:
			return false
		}
	})
}

func parseBaseURLs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var values []string
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &values); err == nil {
			return normalizeBaseURLs(values)
		}
	}
	return normalizeBaseURLs(splitList(raw))
}

func normalizeBaseURLs(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		u := strings.TrimSpace(item)
		u = strings.TrimRight(u, "/")
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func defaultNetEaseAPIBaseURLs() []string {
	return []string{
		"https://apis.netstart.cn/music",
		"https://ncm.zhenxin.me",
		"https://ncmapi.btwoa.com",
		"https://zm.wwoyun.cn",
	}
}

func httpGetJSON(rawURL string, timeoutMs int32, headers map[string]string, target any) error {
	finalHeaders := map[string]string{
		"User-Agent": defaultUserAgent,
		"Accept":     "application/json, text/plain, */*",
	}
	for k, v := range headers {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			continue
		}
		finalHeaders[k] = v
	}

	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:    "GET",
		URL:       rawURL,
		TimeoutMs: timeoutMs,
		Headers:   finalHeaders,
	})
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}
	if len(resp.Body) == 0 {
		return errEmptyResponse
	}
	payload := extractJSONPayload(resp.Body)
	if len(payload) == 0 {
		return errEmptyResponse
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("json decode failed: %w", err)
	}
	return nil
}

func httpPostJSON(rawURL string, body []byte, timeoutMs int32, headers map[string]string, target any) error {
	finalHeaders := map[string]string{
		"User-Agent":   defaultUserAgent,
		"Accept":       "application/json, text/plain, */*",
		"Content-Type": "application/json",
	}
	for k, v := range headers {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			continue
		}
		finalHeaders[k] = v
	}

	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:    "POST",
		URL:       rawURL,
		TimeoutMs: timeoutMs,
		Headers:   finalHeaders,
		Body:      body,
	})
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}
	if len(resp.Body) == 0 {
		return errEmptyResponse
	}
	payload := extractJSONPayload(resp.Body)
	if len(payload) == 0 {
		return errEmptyResponse
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("json decode failed: %w", err)
	}
	return nil
}

func extractJSONPayload(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return trimmed
	}

	objStart := bytes.IndexByte(trimmed, '{')
	objEnd := bytes.LastIndexByte(trimmed, '}')
	arrStart := bytes.IndexByte(trimmed, '[')
	arrEnd := bytes.LastIndexByte(trimmed, ']')

	if objStart >= 0 && objEnd > objStart {
		if arrStart < 0 || objStart < arrStart {
			return trimmed[objStart : objEnd+1]
		}
	}
	if arrStart >= 0 && arrEnd > arrStart {
		return trimmed[arrStart : arrEnd+1]
	}
	return nil
}

func normalizeText(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	replacer := strings.NewReplacer(
		" ", "",
		"\t", "",
		"\n", "",
		"-", "",
		"_", "",
		".", "",
		",", "",
		"，", "",
		"。", "",
		"·", "",
		"・", "",
		"'", "",
		"\"", "",
		"(", "",
		")", "",
		"（", "",
		"）", "",
	)
	return replacer.Replace(v)
}

func scoreName(target, candidate string) int {
	nt := normalizeText(target)
	nc := normalizeText(candidate)
	if nt == "" || nc == "" {
		return 0
	}
	if nt == nc {
		return 100
	}
	if strings.HasPrefix(nc, nt) || strings.HasPrefix(nt, nc) {
		return 80
	}
	if strings.Contains(nc, nt) || strings.Contains(nt, nc) {
		return 60
	}
	return 0
}

func forceHTTPS(raw string) string {
	if strings.HasPrefix(raw, "http://") {
		return "https://" + strings.TrimPrefix(raw, "http://")
	}
	return raw
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func uniqueImageInfos(images []metadata.ImageInfo) []metadata.ImageInfo {
	seen := map[string]struct{}{}
	out := make([]metadata.ImageInfo, 0, len(images))
	for _, img := range images {
		u := strings.TrimSpace(img.URL)
		if u == "" {
			continue
		}
		u = forceHTTPS(u)
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		img.URL = u
		out = append(out, img)
	}
	return out
}

// NetEase adapter

type neteaseAdapter struct {
	client *neteaseClient
}

type neteaseClient struct {
	baseURLs      []string
	loadBalance   string
	currentIndex  uint64
	timeoutMs     int32
}

func newNetEaseAdapter(cfg pluginConfig) *neteaseAdapter {
	return &neteaseAdapter{
		client: &neteaseClient{
			baseURLs:    cfg.neteaseBaseURLs,
			loadBalance: cfg.neteaseLoadBalancing,
			timeoutMs:   cfg.timeoutMs,
		},
	}
}

func (c *neteaseClient) orderedBaseURLs() []string {
	if len(c.baseURLs) == 0 {
		return defaultNetEaseAPIBaseURLs()
	}
	if len(c.baseURLs) == 1 {
		return c.baseURLs
	}
	idx := atomic.AddUint64(&c.currentIndex, 1)
	var start int
	switch c.loadBalance {
	case loadBalanceRoundRobin:
		start = int(idx % uint64(len(c.baseURLs)))
	case loadBalanceRandom:
		fallthrough
	default:
		start = int((idx*1103515245 + 12345) % uint64(len(c.baseURLs)))
	}
	ordered := make([]string, 0, len(c.baseURLs))
	for i := 0; i < len(c.baseURLs); i++ {
		ordered = append(ordered, c.baseURLs[(start+i)%len(c.baseURLs)])
	}
	return ordered
}

func (a *neteaseAdapter) headers() map[string]string {
	return map[string]string{
		"Origin":  "https://music.163.com",
		"Referer": "https://music.163.com",
	}
}

func (a *neteaseAdapter) GetArtistURL(req metadata.ArtistRequest) (*metadata.ArtistURLResponse, error) {
	artist, err := a.findBestArtist(req.Name)
	if err != nil {
		return nil, nil
	}
	return &metadata.ArtistURLResponse{URL: fmt.Sprintf("https://music.163.com/#/artist?id=%d", artist.ID)}, nil
}

func (a *neteaseAdapter) GetArtistBiography(req metadata.ArtistRequest) (*metadata.ArtistBiographyResponse, error) {
	artist, err := a.findBestArtist(req.Name)
	if err != nil {
		return nil, nil
	}
	desc, err := a.getArtistDesc(artist.ID)
	if err != nil {
		return nil, nil
	}
	bio := buildNetEaseBiography(desc)
	if bio == "" {
		return nil, nil
	}
	return &metadata.ArtistBiographyResponse{Biography: bio}, nil
}

func (a *neteaseAdapter) GetArtistImages(req metadata.ArtistRequest) (*metadata.ArtistImagesResponse, error) {
	artist, err := a.findBestArtist(req.Name)
	if err != nil {
		return nil, nil
	}

	picURL := strings.TrimSpace(artist.PicURL)
	img1v1 := strings.TrimSpace(artist.Img1v1URL)
	if picURL == "" {
		detail, err := a.getArtistDetail(artist.ID)
		if err == nil {
			picURL = strings.TrimSpace(detail.Data.Artist.PicURL)
			if img1v1 == "" {
				img1v1 = strings.TrimSpace(detail.Data.Artist.Img1v1URL)
			}
		}
	}

	images := make([]metadata.ImageInfo, 0, 2)
	if picURL != "" {
		images = append(images, metadata.ImageInfo{URL: picURL, Size: 640})
	}
	if img1v1 != "" && img1v1 != picURL {
		images = append(images, metadata.ImageInfo{URL: img1v1, Size: 300})
	}
	images = uniqueImageInfos(images)
	if len(images) == 0 {
		return nil, nil
	}
	return &metadata.ArtistImagesResponse{Images: images}, nil
}

func (a *neteaseAdapter) GetSimilarArtists(req metadata.SimilarArtistsRequest) (*metadata.SimilarArtistsResponse, error) {
	artist, err := a.findBestArtist(req.Name)
	if err != nil {
		return nil, nil
	}

	related, err := a.getSimilarArtists(artist.ID)
	if err != nil || len(related.Artists) == 0 {
		return nil, nil
	}

	limit := int(req.Limit)
	if limit <= 0 || limit > len(related.Artists) {
		limit = len(related.Artists)
	}

	artists := make([]metadata.ArtistRef, 0, limit)
	for _, item := range related.Artists {
		if len(artists) >= limit {
			break
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		artists = append(artists, metadata.ArtistRef{Name: name})
	}
	if len(artists) == 0 {
		return nil, nil
	}
	return &metadata.SimilarArtistsResponse{Artists: artists}, nil
}

func (a *neteaseAdapter) GetArtistTopSongs(req metadata.TopSongsRequest) (*metadata.TopSongsResponse, error) {
	artist, err := a.findBestArtist(req.Name)
	if err != nil {
		return nil, nil
	}

	tracks, err := a.getArtistTopSongs(artist.ID)
	if err != nil || len(tracks.Songs) == 0 {
		return nil, nil
	}

	count := int(req.Count)
	if count <= 0 || count > len(tracks.Songs) {
		count = len(tracks.Songs)
	}

	out := make([]metadata.SongRef, 0, count)
	for _, s := range tracks.Songs {
		if len(out) >= count {
			break
		}
		names := make([]string, 0, len(s.Ar))
		for _, ar := range s.Ar {
			if strings.TrimSpace(ar.Name) != "" {
				names = append(names, strings.TrimSpace(ar.Name))
			}
		}
		out = append(out, metadata.SongRef{
			Name:     strings.TrimSpace(s.Name),
			Artist:   strings.Join(names, ", "),
			Album:    strings.TrimSpace(s.Al.Name),
			Duration: float32(s.Dt) / 1000,
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &metadata.TopSongsResponse{Songs: out}, nil
}

func (a *neteaseAdapter) GetAlbumInfo(req metadata.AlbumRequest) (*metadata.AlbumInfoResponse, error) {
	album, err := a.findBestAlbum(req.Name, req.Artist)
	if err != nil {
		return nil, nil
	}

	detail, err := a.getAlbumDetail(album.ID)
	if err == nil && detail.Code == 200 {
		album = &detail.Album
	}

	resp := &metadata.AlbumInfoResponse{
		Name:        firstNonEmpty(album.Name, req.Name),
		Description: strings.TrimSpace(album.Description),
		URL:         fmt.Sprintf("https://music.163.com/#/album?id=%d", album.ID),
	}
	if strings.TrimSpace(resp.Name) == "" && strings.TrimSpace(resp.Description) == "" && strings.TrimSpace(resp.URL) == "" {
		return nil, nil
	}
	return resp, nil
}

func (a *neteaseAdapter) GetAlbumImages(req metadata.AlbumRequest) (*metadata.AlbumImagesResponse, error) {
	album, err := a.findBestAlbum(req.Name, req.Artist)
	if err != nil {
		return nil, nil
	}
	images := make([]metadata.ImageInfo, 0, 2)
	if strings.TrimSpace(album.PicURL) != "" {
		images = append(images, metadata.ImageInfo{URL: album.PicURL, Size: 640})
	}
	if strings.TrimSpace(album.BlurPicURL) != "" && album.BlurPicURL != album.PicURL {
		images = append(images, metadata.ImageInfo{URL: album.BlurPicURL, Size: 300})
	}
	images = uniqueImageInfos(images)
	if len(images) == 0 {
		return nil, nil
	}
	return &metadata.AlbumImagesResponse{Images: images}, nil
}

func (a *neteaseAdapter) findBestArtist(name string) (*neteaseArtist, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errNotFound
	}
	artists, err := a.searchArtists(name, 8)
	if err != nil || len(artists) == 0 {
		return nil, errNotFound
	}
	best := artists[0]
	bestScore := -1
	for _, c := range artists {
		score := scoreName(name, c.Name)
		if score > bestScore {
			bestScore = score
			best = c
		}
	}
	return &best, nil
}

func (a *neteaseAdapter) findBestAlbum(name, artist string) (*neteaseAlbum, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errNotFound
	}
	albums, err := a.searchAlbums(name, 8)
	if err != nil || len(albums) == 0 {
		return nil, errNotFound
	}
	best := albums[0]
	bestScore := -1
	for _, c := range albums {
		score := scoreName(name, c.Name)
		if artist != "" {
			score += scoreName(artist, c.Artist.Name)
		}
		if score > bestScore {
			bestScore = score
			best = c
		}
	}
	return &best, nil
}

func (a *neteaseAdapter) searchArtists(name string, limit int) ([]neteaseArtist, error) {
	query := url.QueryEscape(strings.TrimSpace(name))
	var lastErr error = errNotFound
	for _, baseURL := range a.client.orderedBaseURLs() {
		sURL := fmt.Sprintf("%s/cloudsearch?keywords=%s&type=100&limit=%d", baseURL, query, limit)
		var result neteaseSearchResult
		err := httpGetJSON(sURL, a.client.timeoutMs, a.headers(), &result)
		if err != nil {
			lastErr = err
			continue
		}
		if result.Code != 200 {
			lastErr = fmt.Errorf("netease search artist code=%d", result.Code)
			continue
		}
		if len(result.Result.Artists) == 0 {
			lastErr = errNotFound
			continue
		}
		return result.Result.Artists, nil
	}
	return nil, lastErr
}

func (a *neteaseAdapter) searchAlbums(name string, limit int) ([]neteaseAlbum, error) {
	query := url.QueryEscape(strings.TrimSpace(name))
	var lastErr error = errNotFound
	for _, baseURL := range a.client.orderedBaseURLs() {
		sURL := fmt.Sprintf("%s/cloudsearch?keywords=%s&type=10&limit=%d", baseURL, query, limit)
		var result neteaseSearchResult
		err := httpGetJSON(sURL, a.client.timeoutMs, a.headers(), &result)
		if err != nil {
			lastErr = err
			continue
		}
		if result.Code != 200 {
			lastErr = fmt.Errorf("netease search album code=%d", result.Code)
			continue
		}
		if len(result.Result.Albums) == 0 {
			lastErr = errNotFound
			continue
		}
		return result.Result.Albums, nil
	}
	return nil, lastErr
}

func (a *neteaseAdapter) getArtistDetail(artistID int) (*neteaseArtistDetail, error) {
	var lastErr error = errNotFound
	for _, baseURL := range a.client.orderedBaseURLs() {
		dURL := fmt.Sprintf("%s/artist/detail?id=%s", baseURL, url.QueryEscape(strconv.Itoa(artistID)))
		var out neteaseArtistDetail
		err := httpGetJSON(dURL, a.client.timeoutMs, a.headers(), &out)
		if err != nil {
			lastErr = err
			continue
		}
		if out.Code == 200 {
			return &out, nil
		}
		lastErr = fmt.Errorf("netease artist detail code=%d", out.Code)
	}
	return nil, lastErr
}

func (a *neteaseAdapter) getArtistDesc(artistID int) (*neteaseArtistDesc, error) {
	var lastErr error = errNotFound
	for _, baseURL := range a.client.orderedBaseURLs() {
		dURL := fmt.Sprintf("%s/artist/desc?id=%s", baseURL, url.QueryEscape(strconv.Itoa(artistID)))
		var out neteaseArtistDesc
		err := httpGetJSON(dURL, a.client.timeoutMs, a.headers(), &out)
		if err != nil {
			lastErr = err
			continue
		}
		if out.Code == 200 {
			return &out, nil
		}
		lastErr = fmt.Errorf("netease artist desc code=%d", out.Code)
	}
	return nil, lastErr
}

func (a *neteaseAdapter) getArtistTopSongs(artistID int) (*neteaseArtistTopSongsResponse, error) {
	var lastErr error = errNotFound
	for _, baseURL := range a.client.orderedBaseURLs() {
		dURL := fmt.Sprintf("%s/artist/top/song?id=%s", baseURL, url.QueryEscape(strconv.Itoa(artistID)))
		var out neteaseArtistTopSongsResponse
		err := httpGetJSON(dURL, a.client.timeoutMs, a.headers(), &out)
		if err != nil {
			lastErr = err
			continue
		}
		if out.Code == 200 && len(out.Songs) > 0 {
			return &out, nil
		}
		lastErr = fmt.Errorf("netease artist top song code=%d", out.Code)
	}
	return nil, lastErr
}

func (a *neteaseAdapter) getSimilarArtists(artistID int) (*neteaseSimilarArtistsResponse, error) {
	var lastErr error = errNotFound
	for _, baseURL := range a.client.orderedBaseURLs() {
		dURL := fmt.Sprintf("%s/simi/artist?id=%s", baseURL, url.QueryEscape(strconv.Itoa(artistID)))
		var out neteaseSimilarArtistsResponse
		err := httpGetJSON(dURL, a.client.timeoutMs, a.headers(), &out)
		if err != nil {
			lastErr = err
			continue
		}
		if out.Code == 200 && len(out.Artists) > 0 {
			return &out, nil
		}
		lastErr = fmt.Errorf("netease similar artist code=%d", out.Code)
	}
	return nil, lastErr
}

func (a *neteaseAdapter) getAlbumDetail(albumID int) (*neteaseAlbumDetail, error) {
	var lastErr error = errNotFound
	for _, baseURL := range a.client.orderedBaseURLs() {
		dURL := fmt.Sprintf("%s/album?id=%s", baseURL, url.QueryEscape(strconv.Itoa(albumID)))
		var out neteaseAlbumDetail
		err := httpGetJSON(dURL, a.client.timeoutMs, a.headers(), &out)
		if err != nil {
			lastErr = err
			continue
		}
		if out.Code == 200 {
			return &out, nil
		}
		lastErr = fmt.Errorf("netease album detail code=%d", out.Code)
	}
	return nil, lastErr
}

func buildNetEaseBiography(desc *neteaseArtistDesc) string {
	if desc == nil {
		return ""
	}
	parts := make([]string, 0, 1+len(desc.Introduction))
	if strings.TrimSpace(desc.BriefDesc) != "" {
		parts = append(parts, strings.TrimSpace(desc.BriefDesc))
	}
	for _, intro := range desc.Introduction {
		title := strings.TrimSpace(intro.Ti)
		text := strings.TrimSpace(intro.Txt)
		if title == "" && text == "" {
			continue
		}
		if title != "" && text != "" {
			parts = append(parts, title+"\n"+text)
		} else {
			parts = append(parts, firstNonEmpty(title, text))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

type neteaseSearchResult struct {
	Result struct {
		Artists []neteaseArtist `json:"artists"`
		Albums  []neteaseAlbum  `json:"albums"`
	} `json:"result"`
	Code int `json:"code"`
}

type neteaseArtist struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	PicURL    string `json:"picUrl"`
	Img1v1URL string `json:"img1v1Url"`
}

type neteaseAlbum struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	PicURL      string `json:"picUrl"`
	BlurPicURL  string `json:"blurPicUrl"`
	Description string `json:"description"`
	Artist      struct {
		Name string `json:"name"`
	} `json:"artist"`
}

type neteaseArtistDetail struct {
	Data struct {
		Artist neteaseArtist `json:"artist"`
	} `json:"data"`
	Code int `json:"code"`
}

type neteaseArtistDesc struct {
	BriefDesc    string `json:"briefDesc"`
	Introduction []struct {
		Ti  string `json:"ti"`
		Txt string `json:"txt"`
	} `json:"introduction"`
	Code int `json:"code"`
}

type neteaseArtistTopSongsResponse struct {
	Songs []struct {
		Name string `json:"name"`
		Dt   int    `json:"dt"`
		Al   struct {
			Name string `json:"name"`
		} `json:"al"`
		Ar []struct {
			Name string `json:"name"`
		} `json:"ar"`
	} `json:"songs"`
	Code int `json:"code"`
}

type neteaseSimilarArtistsResponse struct {
	Artists []neteaseArtist `json:"artists"`
	Code    int             `json:"code"`
}

type neteaseAlbumDetail struct {
	Album neteaseAlbum `json:"album"`
	Code  int          `json:"code"`
}

// QQ adapter

type qqAdapter struct {
	timeoutMs int32
}

func newQQAdapter(cfg pluginConfig) *qqAdapter {
	return &qqAdapter{timeoutMs: cfg.timeoutMs}
}

func (a *qqAdapter) headers() map[string]string {
	return map[string]string{
		"Origin":  "https://y.qq.com",
		"Referer": "https://y.qq.com/",
	}
}

func (a *qqAdapter) GetArtistURL(req metadata.ArtistRequest) (*metadata.ArtistURLResponse, error) {
	singer, err := a.findBestSinger(req.Name)
	if err != nil || singer.Mid == "" {
		return nil, nil
	}
	return &metadata.ArtistURLResponse{URL: "https://y.qq.com/n/ryqq/singer/" + strings.TrimSpace(singer.Mid)}, nil
}

func (a *qqAdapter) GetArtistBiography(req metadata.ArtistRequest) (*metadata.ArtistBiographyResponse, error) {
	singer, err := a.findBestSinger(req.Name)
	if err != nil || singer.Mid == "" {
		return nil, nil
	}
	detail, err := a.getSingerDetail(singer.Mid)
	if err != nil {
		return nil, nil
	}
	bio := strings.TrimSpace(detail.SingerBrief)
	if bio == "" {
		return nil, nil
	}
	return &metadata.ArtistBiographyResponse{Biography: bio}, nil
}

func (a *qqAdapter) GetArtistImages(req metadata.ArtistRequest) (*metadata.ArtistImagesResponse, error) {
	singer, err := a.findBestSinger(req.Name)
	if err != nil {
		return nil, nil
	}
	images := make([]metadata.ImageInfo, 0, 2)
	if strings.TrimSpace(singer.Pic) != "" {
		images = append(images, metadata.ImageInfo{URL: singer.Pic, Size: 300})
	}
	if strings.TrimSpace(singer.Mid) != "" {
		images = append(images,
			metadata.ImageInfo{URL: "https://y.gtimg.cn/music/photo_new/T001R800x800M000" + singer.Mid + ".jpg", Size: 800},
			metadata.ImageInfo{URL: "https://y.gtimg.cn/music/photo_new/T001R300x300M000" + singer.Mid + ".jpg", Size: 300},
		)
	}
	images = uniqueImageInfos(images)
	if len(images) == 0 {
		return nil, nil
	}
	return &metadata.ArtistImagesResponse{Images: images}, nil
}

func (a *qqAdapter) GetArtistTopSongs(req metadata.TopSongsRequest) (*metadata.TopSongsResponse, error) {
	singer, err := a.findBestSinger(req.Name)
	if err != nil || singer.Mid == "" {
		return nil, nil
	}
	detail, err := a.getSingerDetail(singer.Mid)
	if err != nil {
		return nil, nil
	}
	if len(detail.GetSongInfo) == 0 {
		return nil, nil
	}
	count := int(req.Count)
	if count <= 0 || count > len(detail.GetSongInfo) {
		count = len(detail.GetSongInfo)
	}
	out := make([]metadata.SongRef, 0, count)
	for _, s := range detail.GetSongInfo {
		if len(out) >= count {
			break
		}
		artistNames := make([]string, 0, len(s.Singer))
		for _, ar := range s.Singer {
			if strings.TrimSpace(ar.Name) != "" {
				artistNames = append(artistNames, strings.TrimSpace(ar.Name))
			}
		}
		out = append(out, metadata.SongRef{
			Name:     strings.TrimSpace(s.SongName),
			Artist:   strings.Join(artistNames, ", "),
			Album:    strings.TrimSpace(s.AlbumName),
			Duration: float32(s.Interval),
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &metadata.TopSongsResponse{Songs: out}, nil
}

func (a *qqAdapter) GetSimilarArtists(req metadata.SimilarArtistsRequest) (*metadata.SimilarArtistsResponse, error) {
	singer, err := a.findBestSinger(req.Name)
	if err != nil || strings.TrimSpace(singer.Mid) == "" {
		return nil, nil
	}

	limit := int(req.Limit)
	if limit <= 0 {
		limit = defaultSimilarArtistCount
	}

	data, err := a.getSimilarSingerList(singer.Mid, limit)
	if err != nil || len(data.SingerList) == 0 {
		return nil, nil
	}

	out := make([]metadata.ArtistRef, 0, limit)
	seen := map[string]struct{}{}
	for _, item := range data.SingerList {
		if len(out) >= limit {
			break
		}
		name := strings.TrimSpace(item.SingerName)
		if name == "" {
			continue
		}
		key := normalizeText(name)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, metadata.ArtistRef{Name: name})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &metadata.SimilarArtistsResponse{Artists: out}, nil
}

func (a *qqAdapter) GetAlbumInfo(req metadata.AlbumRequest) (*metadata.AlbumInfoResponse, error) {
	album, err := a.findBestAlbum(req.Name, req.Artist)
	if err != nil || album.Mid == "" {
		return nil, nil
	}
	detail, err := a.getAlbumInfo(album.Mid)
	if err != nil {
		return &metadata.AlbumInfoResponse{
			Name: firstNonEmpty(album.Name, req.Name),
			URL:  "https://y.qq.com/n/ryqq/albumDetail/" + album.Mid,
		}, nil
	}
	return &metadata.AlbumInfoResponse{
		Name:        firstNonEmpty(detail.Data.Name, album.Name, req.Name),
		Description: strings.TrimSpace(detail.Data.Desc),
		URL:         "https://y.qq.com/n/ryqq/albumDetail/" + album.Mid,
	}, nil
}

func (a *qqAdapter) GetAlbumImages(req metadata.AlbumRequest) (*metadata.AlbumImagesResponse, error) {
	album, err := a.findBestAlbum(req.Name, req.Artist)
	if err != nil {
		return nil, nil
	}
	images := make([]metadata.ImageInfo, 0, 3)
	if strings.TrimSpace(album.Pic) != "" {
		images = append(images, metadata.ImageInfo{URL: album.Pic, Size: 300})
	}
	if strings.TrimSpace(album.Mid) != "" {
		images = append(images,
			metadata.ImageInfo{URL: "https://y.gtimg.cn/music/photo_new/T002R800x800M000" + album.Mid + ".jpg", Size: 800},
			metadata.ImageInfo{URL: "https://y.gtimg.cn/music/photo_new/T002R500x500M000" + album.Mid + ".jpg", Size: 500},
		)
	}
	images = uniqueImageInfos(images)
	if len(images) == 0 {
		return nil, nil
	}
	return &metadata.AlbumImagesResponse{Images: images}, nil
}

func (a *qqAdapter) findBestSinger(name string) (*qqSingerItem, error) {
	resp, err := a.searchSmartbox(name)
	if err != nil || len(resp.Data.Singer.ItemList) == 0 {
		return nil, errNotFound
	}
	best := resp.Data.Singer.ItemList[0]
	bestScore := -1
	for _, c := range resp.Data.Singer.ItemList {
		score := scoreName(name, firstNonEmpty(c.Name, c.Singer))
		if score > bestScore {
			bestScore = score
			best = c
		}
	}
	return &best, nil
}

func (a *qqAdapter) findBestAlbum(name, artist string) (*qqAlbumItem, error) {
	resp, err := a.searchSmartbox(name)
	if err != nil || len(resp.Data.Album.ItemList) == 0 {
		return nil, errNotFound
	}
	best := resp.Data.Album.ItemList[0]
	bestScore := -1
	for _, c := range resp.Data.Album.ItemList {
		score := scoreName(name, c.Name)
		if artist != "" {
			score += scoreName(artist, c.Singer)
		}
		if score > bestScore {
			bestScore = score
			best = c
		}
	}
	return &best, nil
}

func (a *qqAdapter) searchSmartbox(keyword string) (*qqSmartboxResponse, error) {
	u := "https://c.y.qq.com/splcloud/fcgi-bin/smartbox_new.fcg?key=" + url.QueryEscape(strings.TrimSpace(keyword)) + "&format=json"
	var out qqSmartboxResponse
	if err := httpGetJSON(u, a.timeoutMs, a.headers(), &out); err != nil {
		return nil, err
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("qq smartbox code=%d", out.Code)
	}
	return &out, nil
}

func (a *qqAdapter) getSingerDetail(singerMID string) (*qqSingerDetailResponse, error) {
	u := "https://c.y.qq.com/v8/fcg-bin/fcg_v8_singer_detail_cp.fcg?singermid=" + url.QueryEscape(strings.TrimSpace(singerMID)) + "&format=json"
	var out qqSingerDetailResponse
	if err := httpGetJSON(u, a.timeoutMs, a.headers(), &out); err != nil {
		return nil, err
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("qq singer detail code=%d", out.Code)
	}
	return &out, nil
}

func (a *qqAdapter) getSimilarSingerList(singerMID string, num int) (*qqSimilarSingerData, error) {
	mid := strings.TrimSpace(singerMID)
	if mid == "" {
		return nil, errNotFound
	}
	if num <= 0 {
		num = defaultSimilarArtistCount
	}

	reqBody := map[string]any{
		"comm": map[string]any{"ct": 24, "cv": 0},
		"req_1": map[string]any{
			"module": "music.SimilarSingerSvr",
			"method": "GetSimilarSingerList",
			"param":  map[string]any{"singerMid": mid, "num": num},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	var out qqSimilarSingerResponse
	if err := httpPostJSON("https://u.y.qq.com/cgi-bin/musicu.fcg", body, a.timeoutMs, a.headers(), &out); err != nil {
		return nil, err
	}
	if out.Req1.Code != 0 {
		return nil, fmt.Errorf("qq similar singer code=%d", out.Req1.Code)
	}
	if out.Req1.Data.Code != 0 {
		return nil, fmt.Errorf("qq similar singer data code=%d", out.Req1.Data.Code)
	}
	if len(out.Req1.Data.SingerList) == 0 {
		return nil, errNotFound
	}
	return &out.Req1.Data, nil
}

func (a *qqAdapter) getAlbumInfo(albumMID string) (*qqAlbumInfoResponse, error) {
	u := "https://c.y.qq.com/v8/fcg-bin/fcg_v8_album_info_cp.fcg?albummid=" + url.QueryEscape(strings.TrimSpace(albumMID)) + "&format=json"
	var out qqAlbumInfoResponse
	if err := httpGetJSON(u, a.timeoutMs, a.headers(), &out); err != nil {
		return nil, err
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("qq album info code=%d", out.Code)
	}
	return &out, nil
}

type qqSmartboxResponse struct {
	Code int `json:"code"`
	Data struct {
		Singer struct {
			ItemList []qqSingerItem `json:"itemlist"`
		} `json:"singer"`
		Album struct {
			ItemList []qqAlbumItem `json:"itemlist"`
		} `json:"album"`
	} `json:"data"`
}

type qqSingerItem struct {
	Name   string `json:"name"`
	Singer string `json:"singer"`
	Mid    string `json:"mid"`
	Pic    string `json:"pic"`
}

type qqAlbumItem struct {
	Name   string `json:"name"`
	Singer string `json:"singer"`
	Mid    string `json:"mid"`
	Pic    string `json:"pic"`
}

type qqSingerDetailResponse struct {
	Code        int    `json:"code"`
	SingerBrief string `json:"singerBrief"`
	GetSongInfo []struct {
		SongName  string `json:"songname"`
		AlbumName string `json:"albumname"`
		Interval  int    `json:"interval"`
		Singer    []struct {
			Name string `json:"name"`
		} `json:"singer"`
	} `json:"getSongInfo"`
}

type qqAlbumInfoResponse struct {
	Code int `json:"code"`
	Data struct {
		Name string `json:"name"`
		Desc string `json:"desc"`
	} `json:"data"`
}

type qqSimilarSingerResponse struct {
	Req1 struct {
		Code int                `json:"code"`
		Data qqSimilarSingerData `json:"data"`
	} `json:"req_1"`
}

type qqSimilarSingerData struct {
	Code       int                   `json:"code"`
	ErrMsg     string                `json:"errMsg"`
	SingerList []qqSimilarSingerItem `json:"singerlist"`
}

type qqSimilarSingerItem struct {
	SingerID   int64  `json:"singerId"`
	SingerMid  string `json:"singerMid"`
	SingerName string `json:"singerName"`
	SingerPic  string `json:"singerPic"`
}

// MusicBrainz adapter

type musicBrainzAdapter struct {
	timeoutMs int32
}

func newMusicBrainzAdapter(cfg pluginConfig) *musicBrainzAdapter {
	return &musicBrainzAdapter{timeoutMs: cfg.timeoutMs}
}

func (a *musicBrainzAdapter) headers() map[string]string {
	return map[string]string{}
}

func (a *musicBrainzAdapter) GetArtistURL(req metadata.ArtistRequest) (*metadata.ArtistURLResponse, error) {
	artist, err := a.findBestArtist(req.Name)
	if err != nil || artist.ID == "" {
		return nil, nil
	}
	return &metadata.ArtistURLResponse{URL: "https://musicbrainz.org/artist/" + artist.ID}, nil
}

func (a *musicBrainzAdapter) GetArtistBiography(req metadata.ArtistRequest) (*metadata.ArtistBiographyResponse, error) {
	artist, err := a.findBestArtist(req.Name)
	if err != nil {
		return nil, nil
	}
	bio := buildMusicBrainzArtistBio(artist)
	if bio == "" {
		return nil, nil
	}
	return &metadata.ArtistBiographyResponse{Biography: bio}, nil
}

func (a *musicBrainzAdapter) GetArtistImages(_ metadata.ArtistRequest) (*metadata.ArtistImagesResponse, error) {
	return nil, nil
}

func (a *musicBrainzAdapter) GetArtistTopSongs(_ metadata.TopSongsRequest) (*metadata.TopSongsResponse, error) {
	return nil, nil
}

func (a *musicBrainzAdapter) GetSimilarArtists(req metadata.SimilarArtistsRequest) (*metadata.SimilarArtistsResponse, error) {
	artistID := strings.TrimSpace(req.MBID)
	if artistID == "" {
		artist, err := a.findBestArtist(req.Name)
		if err != nil || strings.TrimSpace(artist.ID) == "" {
			return nil, nil
		}
		artistID = strings.TrimSpace(artist.ID)
	}

	artist, err := a.getArtistDetail(artistID)
	if err != nil || len(artist.Relations) == 0 {
		return nil, nil
	}

	limit := int(req.Limit)
	if limit <= 0 {
		limit = defaultSimilarArtistCount
	}
	out := make([]metadata.ArtistRef, 0, limit)
	seen := map[string]struct{}{}
	for _, rel := range artist.Relations {
		if !isMusicBrainzSimilarRelationType(rel.Type) {
			continue
		}
		if rel.TargetType != "" && strings.ToLower(strings.TrimSpace(rel.TargetType)) != "artist" {
			continue
		}
		name := strings.TrimSpace(rel.Artist.Name)
		if name == "" {
			continue
		}
		mbid := strings.TrimSpace(rel.Artist.ID)
		key := normalizeText(name) + "|" + strings.ToLower(mbid)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, metadata.ArtistRef{Name: name, MBID: mbid})
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &metadata.SimilarArtistsResponse{Artists: out}, nil
}

func (a *musicBrainzAdapter) GetAlbumInfo(req metadata.AlbumRequest) (*metadata.AlbumInfoResponse, error) {
	rg, err := a.findBestReleaseGroup(req.Name, req.Artist)
	if err != nil {
		return nil, nil
	}
	descParts := make([]string, 0, 3)
	if strings.TrimSpace(rg.Disambiguation) != "" {
		descParts = append(descParts, strings.TrimSpace(rg.Disambiguation))
	}
	if strings.TrimSpace(rg.PrimaryType) != "" {
		descParts = append(descParts, "Type: "+strings.TrimSpace(rg.PrimaryType))
	}
	if strings.TrimSpace(rg.FirstReleaseDate) != "" {
		descParts = append(descParts, "First release: "+strings.TrimSpace(rg.FirstReleaseDate))
	}
	return &metadata.AlbumInfoResponse{
		Name:        firstNonEmpty(rg.Title, req.Name),
		MBID:        strings.TrimSpace(rg.ID),
		Description: strings.Join(descParts, "\n"),
		URL:         "https://musicbrainz.org/release-group/" + rg.ID,
	}, nil
}

func (a *musicBrainzAdapter) GetAlbumImages(req metadata.AlbumRequest) (*metadata.AlbumImagesResponse, error) {
	rg, err := a.findBestReleaseGroup(req.Name, req.Artist)
	if err != nil || rg.ID == "" {
		return nil, nil
	}
	cover, err := a.getReleaseGroupCoverArt(rg.ID)
	if err != nil || len(cover.Images) == 0 {
		return nil, nil
	}
	images := make([]metadata.ImageInfo, 0, len(cover.Images)*3)
	for _, img := range cover.Images {
		if strings.TrimSpace(img.Image) != "" {
			images = append(images, metadata.ImageInfo{URL: img.Image, Size: 1200})
		}
		if strings.TrimSpace(img.Thumbnails.Large) != "" {
			images = append(images, metadata.ImageInfo{URL: img.Thumbnails.Large, Size: 500})
		}
		if strings.TrimSpace(img.Thumbnails.Small) != "" {
			images = append(images, metadata.ImageInfo{URL: img.Thumbnails.Small, Size: 250})
		}
	}
	images = uniqueImageInfos(images)
	if len(images) == 0 {
		return nil, nil
	}
	return &metadata.AlbumImagesResponse{Images: images}, nil
}

func (a *musicBrainzAdapter) findBestArtist(name string) (*mbArtist, error) {
	query := "artist:" + strings.TrimSpace(name)
	u := "https://musicbrainz.org/ws/2/artist?query=" + url.QueryEscape(query) + "&fmt=json&limit=8"
	var out mbArtistSearchResponse
	if err := httpGetJSON(u, a.timeoutMs, a.headers(), &out); err != nil {
		return nil, err
	}
	if len(out.Artists) == 0 {
		return nil, errNotFound
	}
	best := out.Artists[0]
	bestScore := -1
	for _, c := range out.Artists {
		score := scoreName(name, c.Name)
		if score > bestScore {
			bestScore = score
			best = c
		}
	}
	return &best, nil
}

func (a *musicBrainzAdapter) findBestReleaseGroup(albumName, artistName string) (*mbReleaseGroup, error) {
	albumName = strings.TrimSpace(albumName)
	if albumName == "" {
		return nil, errNotFound
	}
	query := "release:" + albumName
	if strings.TrimSpace(artistName) != "" {
		query += " AND artist:" + strings.TrimSpace(artistName)
	}
	u := "https://musicbrainz.org/ws/2/release-group?query=" + url.QueryEscape(query) + "&fmt=json&limit=8"
	var out mbReleaseGroupSearchResponse
	if err := httpGetJSON(u, a.timeoutMs, a.headers(), &out); err != nil {
		return nil, err
	}
	if len(out.ReleaseGroups) == 0 {
		return nil, errNotFound
	}
	best := out.ReleaseGroups[0]
	bestScore := -1
	for _, c := range out.ReleaseGroups {
		score := scoreName(albumName, c.Title)
		if artistName != "" {
			score += scoreName(artistName, firstArtistCreditName(c.ArtistCredit))
		}
		if score > bestScore {
			bestScore = score
			best = c
		}
	}
	return &best, nil
}

func (a *musicBrainzAdapter) getArtistDetail(artistID string) (*mbArtist, error) {
	artistID = strings.TrimSpace(artistID)
	if artistID == "" {
		return nil, errNotFound
	}
	u := "https://musicbrainz.org/ws/2/artist/" + url.QueryEscape(artistID) + "?fmt=json&inc=artist-rels"
	var out mbArtist
	if err := httpGetJSON(u, a.timeoutMs, a.headers(), &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.ID) == "" {
		return nil, errNotFound
	}
	return &out, nil
}

func (a *musicBrainzAdapter) getReleaseGroupCoverArt(releaseGroupID string) (*mbCoverArtResponse, error) {
	u := "https://coverartarchive.org/release-group/" + url.QueryEscape(strings.TrimSpace(releaseGroupID))
	var out mbCoverArtResponse
	if err := httpGetJSON(u, a.timeoutMs, a.headers(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func firstArtistCreditName(credits []mbArtistCredit) string {
	for _, c := range credits {
		if strings.TrimSpace(c.Name) != "" {
			return strings.TrimSpace(c.Name)
		}
	}
	return ""
}

func isMusicBrainzSimilarRelationType(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return false
	}
	return strings.Contains(v, "similar")
}

func buildMusicBrainzArtistBio(artist *mbArtist) string {
	if artist == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if strings.TrimSpace(artist.Disambiguation) != "" {
		parts = append(parts, strings.TrimSpace(artist.Disambiguation))
	}

	meta := make([]string, 0, 3)
	if strings.TrimSpace(artist.Type) != "" {
		meta = append(meta, strings.TrimSpace(artist.Type))
	}
	if strings.TrimSpace(artist.Country) != "" {
		meta = append(meta, strings.ToUpper(strings.TrimSpace(artist.Country)))
	}
	life := strings.TrimSpace(artist.LifeSpan.Begin)
	if life != "" {
		if strings.TrimSpace(artist.LifeSpan.End) != "" {
			life += " - " + strings.TrimSpace(artist.LifeSpan.End)
		}
		meta = append(meta, life)
	}
	if len(meta) > 0 {
		parts = append(parts, strings.Join(meta, " | "))
	}

	if len(artist.Tags) > 0 {
		sorted := append([]mbTag(nil), artist.Tags...)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Count > sorted[j].Count
		})
		names := make([]string, 0, 5)
		for _, tag := range sorted {
			if strings.TrimSpace(tag.Name) == "" {
				continue
			}
			names = append(names, strings.TrimSpace(tag.Name))
			if len(names) >= 5 {
				break
			}
		}
		if len(names) > 0 {
			parts = append(parts, "Tags: "+strings.Join(names, ", "))
		}
	}

	return strings.TrimSpace(strings.Join(parts, "\n"))
}

type mbArtistSearchResponse struct {
	Artists []mbArtist `json:"artists"`
}

type mbArtist struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Country       string `json:"country"`
	Disambiguation string `json:"disambiguation"`
	Relations     []mbArtistRelation `json:"relations"`
	LifeSpan      struct {
		Begin string `json:"begin"`
		End   string `json:"end"`
	} `json:"life-span"`
	Tags []mbTag `json:"tags"`
}

type mbArtistRelation struct {
	Type       string      `json:"type"`
	TargetType string      `json:"target-type"`
	Artist     mbArtistRef `json:"artist"`
}

type mbArtistRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type mbTag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type mbReleaseGroupSearchResponse struct {
	ReleaseGroups []mbReleaseGroup `json:"release-groups"`
}

type mbReleaseGroup struct {
	ID               string           `json:"id"`
	Title            string           `json:"title"`
	PrimaryType      string           `json:"primary-type"`
	FirstReleaseDate string           `json:"first-release-date"`
	Disambiguation   string           `json:"disambiguation"`
	ArtistCredit     []mbArtistCredit `json:"artist-credit"`
}

type mbArtistCredit struct {
	Name string `json:"name"`
}

type mbCoverArtResponse struct {
	Images []struct {
		Image      string `json:"image"`
		Thumbnails struct {
			Small string `json:"small"`
			Large string `json:"large"`
		} `json:"thumbnails"`
	} `json:"images"`
}

func main() {}
