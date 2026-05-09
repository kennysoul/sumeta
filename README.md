# sumeta

sumeta is a Navidrome metadata plugin that aggregates metadata from multiple sources:

- NetEase
- QQ Music
- MusicBrainz

It supports:

- Manual source priority order
- Optional fallback to the next source
- Field-level enable/disable (for example, fetch only artist biography)

## Supported metadata methods

- `nd_get_artist_url`
- `nd_get_artist_biography`
- `nd_get_artist_images`
- `nd_get_artist_top_songs`
- `nd_get_album_info`
- `nd_get_album_images`

## Configuration keys

All keys are optional. If not provided, defaults are used.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `SourceOrder` | string list | `netease,qq,musicbrainz` | Source priority order. Accepted names: `netease`, `qq`, `musicbrainz` |
| `EnableFallback` | bool | `true` | If `true`, continue to the next source when current source returns empty |
| `EnabledFields` | string list | all enabled | Explicitly enable only these fields |
| `DisabledFields` | string list | empty | Disable selected fields |
| `EnableArtistURL` | bool | `true` | Toggle artist URL |
| `EnableArtistBiography` | bool | `true` | Toggle artist biography |
| `EnableArtistImages` | bool | `true` | Toggle artist images |
| `EnableArtistTopSongs` | bool | `true` | Toggle artist top songs |
| `EnableAlbumInfo` | bool | `true` | Toggle album info |
| `EnableAlbumImages` | bool | `true` | Toggle album images |
| `EnableNetEase` | bool | `true` | Enable NetEase source |
| `EnableQQ` | bool | `true` | Enable QQ source |
| `EnableMusicBrainz` | bool | `true` | Enable MusicBrainz source |
| `TimeoutMs` | int | `15000` | HTTP timeout in milliseconds |
| `APIUrls` / `NetEaseAPIUrls` | string list | built-in list | Override NetEase API base URLs |
| `LoadBalanceMode` / `NetEaseLoadBalanceMode` | string | `random` | NetEase API load balancing mode: `random` or `roundrobin` |

### Field names for `EnabledFields` / `DisabledFields`

- `artist_url`
- `artist_biography`
- `artist_images`
- `artist_top_songs`
- `album_info`
- `album_images`

## Example configurations

Fetch only artist biography, with ordered fallback:

- `SourceOrder=qq,netease,musicbrainz`
- `EnableFallback=true`
- `EnabledFields=artist_biography`

Disable top songs and album images:

- `DisabledFields=artist_top_songs,album_images`

Use custom NetEase API hosts:

- `APIUrls=https://apis.netstart.cn/music,https://ncm.zhenxin.me`
- `LoadBalanceMode=roundrobin`

## Build

```bash
go mod tidy
tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
zip -j sumeta.ndp manifest.json plugin.wasm
```

## Install

1. Put `sumeta.ndp` into Navidrome plugins folder.
2. Enable plugins in Navidrome config.
3. Enable `sumeta` in Navidrome UI.
4. Configure plugin keys based on your use case.
