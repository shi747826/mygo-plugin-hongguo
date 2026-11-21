module github.com/shi747826/mygo-plugin-hongguo

go 1.27.3

require github.com/beilunyang/openhongguo v0.4.0

// Pinned upstream PR #16: video_tab fallback for HTTP 200 empty playback responses.
replace github.com/beilunyang/openhongguo => github.com/maddie/openhongguo v0.4.1-0.20261120080321-3dc401af105c
