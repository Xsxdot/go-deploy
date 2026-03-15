package core

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Xsxdot/go-deploy/pkg/maputil"

	"golang.org/x/sync/errgroup"
)

// ParallelOptions 并发与容错控制参数
type ParallelOptions struct {
	BatchSize        int           // 每次并发数，0 表示全量并发
	Retries          int           // 失败重试次数
	RetryDelay       time.Duration // 每次重试间隔
	TolerateFailures float64       // 容忍度：0~1 为百分比，>1 为绝对数量
	StepName         string       // 当前步骤名，供 RunParallel 日志使用
}

// ParseParallelOptions 从 Step 解析并发选项，支持 strategy: "rolling" 等价于 batch_size: 1
func ParseParallelOptions(step Step) ParallelOptions {
	opts := ParallelOptions{
		BatchSize:        step.BatchSize,
		Retries:          step.Retries,
		TolerateFailures: 0,
	}

	// retry_delay
	if step.RetryDelay != "" {
		if d, err := time.ParseDuration(step.RetryDelay); err == nil {
			opts.RetryDelay = d
		}
	}

	// tolerate_failures: "5%" -> 0.05, "2" -> 2.0；未配置时默认 5%
	if step.TolerateFailures != "" {
		s := strings.TrimSpace(step.TolerateFailures)
		if strings.HasSuffix(s, "%") {
			n, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
			if err == nil && n >= 0 {
				opts.TolerateFailures = n / 100
			}
		} else {
			n, err := strconv.ParseFloat(s, 64)
			if err == nil && n >= 0 {
				opts.TolerateFailures = n
			}
		}
	} else {
		opts.TolerateFailures = 0.05
	}

	// strategy: "rolling" -> batch_size: 1
	if step.With != nil {
		if strategy := maputil.GetString(step.With, "strategy"); strategy == "rolling" {
			opts.BatchSize = 1
		}
	}

	return opts
}

// RunParallel 终极并发脚手架：FilterHealthy -> 信号量 -> 重试 -> MarkDead/容忍度判断
func RunParallel(ctx *DeployContext, targets []Target, opts ParallelOptions, fn func(ctx context.Context, t Target) error) error {
	healthyTargets := ctx.FilterHealthy(targets)
	total := len(healthyTargets)
	if total == 0 {
		return nil
	}

	var sem chan struct{}
	if opts.BatchSize > 0 {
		sem = make(chan struct{}, opts.BatchSize)
	}

	var failedCount int32
	eg, egCtx := errgroup.WithContext(ctx.Context)

	for _, t := range healthyTargets {
		target := t

		eg.Go(func() error {
			if sem != nil {
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-egCtx.Done():
					return egCtx.Err()
				}
			}

			var err error
			for attempt := 0; attempt <= opts.Retries; attempt++ {
				if attempt > 0 {
					select {
					case <-time.After(opts.RetryDelay):
					case <-egCtx.Done():
						return egCtx.Err()
					}
				}

				err = fn(egCtx, target)
				if err == nil {
					break
				}
			}

			if err != nil {
				ctx.MarkDead(target.ID())
				ctx.LogWarn(opts.StepName, target.ID(), "节点已标记为失效")
				currentFails := atomic.AddInt32(&failedCount, 1)

				var allowedFails int
				if opts.TolerateFailures > 0 && opts.TolerateFailures < 1 {
					allowedFails = int(math.Floor(float64(total) * opts.TolerateFailures))
				} else {
					allowedFails = int(opts.TolerateFailures)
				}

				if int(currentFails) > allowedFails {
					ctx.LogError(opts.StepName, target.ID(), fmt.Sprintf("容忍度超限，目标完全失败: %v", err))
					return fmt.Errorf("tolerance exceeded: target %s completely failed: %v", target.ID(), err)
				}

				return nil
			}

			return nil
		})
	}

	return eg.Wait()
}
