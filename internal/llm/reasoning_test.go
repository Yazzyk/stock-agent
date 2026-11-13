package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 本文件覆盖 A 项在适配层的两类行为：
//  1) 只有 reasoning_content 的流，Reply.Message.Content 要填上推理内容；
//  2) len(chunks)==0 与「有 chunk 但全空」必须是两种不同的错误文案。

// newReasoningService 起一个 OpenAI 兼容服务，用给定 SSE 片段回放，并保存为默认模型。
func newReasoningService(t *testing.T, chunks []string) *Service {
	t.Helper()
	srv := fakeChatServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			_, _ = w.Write([]byte(c))
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	svc, _, _ := newTestService(t)
	if _, _, err := svc.Save(SaveInput{Provider: "openai", Model: "gpt-test", BaseURL: srv.URL + "/v1", APIKey: "sk-test", SetDefault: true}); err != nil {
		t.Fatal(err)
	}
	return svc
}

// sseChunk 生成一个 OpenAI 流式片段（delta 里可带 reasoning_content）。
func sseChunk(t *testing.T, delta map[string]any, finish any) string {
	t.Helper()
	chunk := map[string]any{
		"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "gpt-test",
		"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(b) + "\n\n"
}

// TestStreamReasoningOnlyFillsContent 只有 reasoning_content 的流：
// Content 必须填上推理内容并带标注，FinishReason 要带回 finish_reason。
func TestStreamReasoningOnlyFillsContent(t *testing.T) {
	svc := newReasoningService(t, []string{
		sseChunk(t, map[string]any{"role": "assistant", "reasoning_content": "先看规则："}, nil),
		sseChunk(t, map[string]any{"reasoning_content": "9:15-9:20 可撤单"}, "length"),
		"data: [DONE]\n\n",
	})
	reply, err := svc.Stream(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "什么是集合竞价"}},
	}, nil)
	if err != nil {
		t.Fatalf("只有推理内容不应该报错（要回退成正文）: %v", err)
	}
	if !strings.Contains(reply.Message.Content, reasoningFallbackNote) {
		t.Fatalf("Content 必须带推理回退标注 %q，实际 %q", reasoningFallbackNote, reply.Message.Content)
	}
	if !strings.Contains(reply.Message.Content, "9:15-9:20 可撤单") {
		t.Fatalf("推理内容不能丢，实际 %q", reply.Message.Content)
	}
	if reply.Message.FinishReason != "length" {
		t.Fatalf("FinishReason 必须透传，实际 %q", reply.Message.FinishReason)
	}
	if !strings.Contains(reply.Message.ReasoningContent, "9:15-9:20") {
		t.Fatalf("原始推理内容也要原样带回，实际 %q", reply.Message.ReasoningContent)
	}
}

// TestStreamReasoningOnlyCallsOnDelta 推理回退时也要推一次 delta，
// 否则前端流式气泡会一直空白（用户以为卡住了）。
func TestStreamReasoningOnlyCallsOnDelta(t *testing.T) {
	svc := newReasoningService(t, []string{
		sseChunk(t, map[string]any{"reasoning_content": "思维链内容"}, "stop"),
		"data: [DONE]\n\n",
	})
	var deltas []string
	_, err := svc.Stream(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "hi"}},
	}, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	joined := strings.Join(deltas, "")
	if !strings.Contains(joined, "思维链内容") || !strings.Contains(joined, reasoningFallbackNote) {
		t.Fatalf("回退时也要把标注+推理内容推给前端，实际 deltas=%q", joined)
	}
}

// TestStreamNoChunksIsDistinctError 一个 chunk 都没有：文案要指出\"连接/请求\"层面的问题。
func TestStreamNoChunksIsDistinctError(t *testing.T) {
	svc := newReasoningService(t, nil) // 直接关流，零 chunk
	_, err := svc.Stream(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("零 chunk 必须报错")
	}
	if !strings.Contains(err.Error(), "chunk") {
		t.Fatalf("零 chunk 的错误应点明 chunk 层问题，实际: %v", err)
	}
	if strings.Contains(err.Error(), "正文与推理内容都是空的") {
		t.Fatalf("零 chunk 不能和\"全空 chunk\"共用文案: %v", err)
	}
}

// TestStreamAllEmptyChunksIsDistinctError 有 chunk 但全空：
// 文案必须与零 chunk 不同，并按 finish_reason 细分。
func TestStreamAllEmptyChunksIsDistinctError(t *testing.T) {
	svc := newReasoningService(t, []string{
		sseChunk(t, map[string]any{"role": "assistant"}, nil),
		sseChunk(t, map[string]any{}, "stop"),
		"data: [DONE]\n\n",
	})
	_, err := svc.Stream(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("全空 chunk 必须报错")
	}
	for _, want := range []string{"空 chunk", "finish_reason"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("全空错误缺少 %q，实际: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "没有返回任何 chunk") {
		t.Fatalf("全空 chunk 不能和零 chunk 共用文案: %v", err)
	}
}

// TestStreamEmptyWithLengthFinishPointsAtBudget finish_reason=length 的空回复：
// 文案必须指向输出预算（推理模型吃满 max_tokens），而不是笼统的\"没返回内容\"。
func TestStreamEmptyWithLengthFinishPointsAtBudget(t *testing.T) {
	svc := newReasoningService(t, []string{
		sseChunk(t, map[string]any{"role": "assistant"}, "length"),
		"data: [DONE]\n\n",
	})
	_, err := svc.Stream(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("空回复必须报错")
	}
	for _, want := range []string{"finish_reason=length", "推理模型", "MaxTokens"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("length 空回复应给出预算建议（缺 %q），实际: %v", want, err)
		}
	}
}

// TestStreamNormalReplyUnchanged 正常流：正文原样、不带标注、FinishReason 正确。
func TestStreamNormalReplyUnchanged(t *testing.T) {
	svc := newReasoningService(t, []string{
		sseChunk(t, map[string]any{"role": "assistant", "content": "po"}, nil),
		sseChunk(t, map[string]any{"content": "ng"}, "stop"),
		"data: [DONE]\n\n",
	})
	reply, err := svc.Stream(context.Background(), contract.ChatRequest{
		Messages: []contract.Message{{Role: contract.RoleUser, Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	if reply.Message.Content != "pong" {
		t.Fatalf("正常正文不应被改动，实际 %q", reply.Message.Content)
	}
	if strings.Contains(reply.Message.Content, reasoningFallbackNote) {
		t.Fatalf("正常正文不该带推理标注: %q", reply.Message.Content)
	}
	if reply.Message.FinishReason != "stop" {
		t.Fatalf("FinishReason 应为 stop，实际 %q", reply.Message.FinishReason)
	}
}
