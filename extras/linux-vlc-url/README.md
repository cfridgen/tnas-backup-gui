# vlc://-Links unter Linux

VLC meldet sich unter Linux nicht für `vlc://`-Links an. Dieser kleine Vermittler macht den Knopf
**„In VLC öffnen“** in der Dateiansicht zu einem echten Ein-Klick (ohne .m3u-Download).
Es werden nur `http(s)`-Links auf `/stream/…` weitergegeben.

```sh
install -m 755 vlc-url ~/.local/bin/vlc-url          # ~/.local/bin muss im PATH sein
install -m 644 vlc-url.desktop ~/.local/share/applications/
update-desktop-database ~/.local/share/applications
xdg-mime default vlc-url.desktop x-scheme-handler/vlc
```

Ohne Vermittler funktioniert weiterhin „Im Videoplayer öffnen“ (Abspielliste, mit VLC verknüpft).
