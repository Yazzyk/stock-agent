package datasource

// 官方指数域：同花顺指数目录、成分股、指数行情、个股行业/板块反查。
//
// 指数代码后缀：标准指数 .SH/.SZ（如 000001.SH），同花顺指数 .TI（如 885431.TI）。
// ParseCode 不识别 .TI，因此指数成分股/目录类接口直接接收官方 thscode 字符串，
// 本文件在入口处只做 trim + 大写，不强行走个股代码归一。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// FuyaoIndexCatalog 取同花顺指数目录。
// tag：cn_concept（概念）/industry（行业）/region（地域）/tszs（特色），默认 cn_concept。
func (s *Service) FuyaoIndexCatalog(ctx context.Context, tag string) ([]map[string]any, error) {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		tag = "cn_concept"
	}
	switch tag {
	case "cn_concept", "industry", "region", "tszs":
	default:
		return nil, fmt.Errorf("datasource: tag 只支持 cn_concept/industry/region/tszs")
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share-index.catalog.ths-index-list",
		Params:   map[string]string{"tag": tag},
		Limit:    fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoIndexConstituents 取一个指数/板块的成分股（单次仅一个指数 thscode）。
func (s *Service) FuyaoIndexConstituents(ctx context.Context, indexCode string) ([]map[string]any, error) {
	indexCode = strings.ToUpper(strings.TrimSpace(indexCode))
	if indexCode == "" {
		return nil, fmt.Errorf("datasource: indexCode 不能为空")
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share-index.constituents.ths-stock-list",
		Params:   map[string]string{"thscode": indexCode},
		Limit:    fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoIndexMemberships 批量反查个股所属行业/板块（单次最多 100 个代码）。
func (s *Service) FuyaoIndexMemberships(ctx context.Context, codes []string, typ string) ([]map[string]any, error) {
	thsCodes := make([]string, 0, len(codes))
	for _, raw := range codes {
		code, err := parseFuyaoCode(raw)
		if err != nil {
			return nil, err
		}
		if ths := code.THSCode(); ths != "" {
			thsCodes = append(thsCodes, ths)
		}
	}
	if len(thsCodes) == 0 {
		return nil, fmt.Errorf("datasource: codes 不能为空")
	}
	params := map[string]string{"thscodes": strings.Join(thsCodes, ",")}
	if t := strings.TrimSpace(typ); t != "" {
		params["type"] = t
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share-index.constituents.ths-index-list-by-stock",
		Params:   params,
		Limit:    fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// FuyaoIndexSnapshot 取指数行情快照（字段与个股快照一致）。
// volume 单位「股」；turnover 单位「元」。
func (s *Service) FuyaoIndexSnapshot(ctx context.Context, codes []string) ([]contract.FuyaoQuoteRow, error) {
	if len(codes) == 0 {
		return nil, fmt.Errorf("datasource: codes 不能为空")
	}
	thsCodes := make([]string, 0, len(codes))
	for _, raw := range codes {
		raw = strings.ToUpper(strings.TrimSpace(raw))
		if raw == "" {
			continue
		}
		thsCodes = append(thsCodes, raw)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share-index.prices.snapshot",
		Params:   map[string]string{"thscodes": strings.Join(thsCodes, ",")},
		Limit:    fuyaoQueryMaxLimit,
	})
	if err != nil {
		return nil, err
	}
	rows, err := decodeFuyaoItems[contract.FuyaoQuoteRow](data.Items)
	if err != nil {
		return nil, fmt.Errorf("datasource: 指数快照字段解析失败: %w", err)
	}
	return rows, nil
}

// FuyaoIndexKline 取指数历史日线（批量最多 10 个，仅 1d、近 5 年窗口）。
func (s *Service) FuyaoIndexKline(ctx context.Context, codes []string, limit int) ([]contract.FuyaoBarsSeries, error) {
	return s.FuyaoKline(ctx, codes, "1d", time.Time{}, time.Time{}, "none", "index", limit)
}
