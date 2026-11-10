package progressive

import (
	"context"
	"fmt"
)

// contextBackground 是测试里的小工具，避免每处都写 context.Background()。
func contextBackground() context.Context { return context.Background() }

func sprint(v any) string { return fmt.Sprint(v) }
