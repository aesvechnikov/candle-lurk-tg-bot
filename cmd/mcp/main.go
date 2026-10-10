package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/aesvechnikov/candle-lurk-tg-bot/internal/backend"
)

func main() {
	addr := os.Getenv("BACKEND_ADDR")
	if addr == "" {
		addr = "127.0.0.1:9090"
	}
	client := backend.NewClient(addr)

	s := server.NewMCPServer("tg-search", "1.0.0")

	tool := mcp.NewTool("search_tg",
		mcp.WithDescription("Поиск сообщений в Telegram-каналах пользователя по ключевому запросу. Возвращает список найденных сообщений со ссылками t.me."),
		mcp.WithString("user_id", mcp.Description("Telegram user id, от имени которого ведётся поиск"), mcp.Required()),
		mcp.WithArray("channels", mcp.Description("Список имён каналов (с @ или без)")),
		mcp.WithString("query", mcp.Description("Поисковый запрос (слово или короткая фраза)"), mcp.Required()),
		mcp.WithNumber("limit", mcp.Description("Максимум результатов на канал (по умолчанию 20)")),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		userIDStr, err := req.RequireString("user_id")
		if err != nil {
			return mcp.NewToolResultError("user_id is required"), nil
		}
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError("query is required"), nil
		}

		var userID int64
		if _, err := fmt.Sscanf(userIDStr, "%d", &userID); err != nil {
			return mcp.NewToolResultError("user_id must be an integer"), nil
		}

		channels := req.GetStringSlice("channels", nil)
		limit := req.GetInt("limit", 20)

		results, err := client.Search(ctx, userID, channels, query, limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(results) == 0 {
			return mcp.NewToolResultText("no results"), nil
		}

		b, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	})

	if err := server.ServeStdio(s); err != nil {
		log.Fatal(err)
	}
}
