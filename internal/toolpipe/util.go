package toolpipe

import "time"

func millisDuration(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }
