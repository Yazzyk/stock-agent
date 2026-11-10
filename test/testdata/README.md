# test/testdata：真实响应夹具

这些文件是 **2026-11-10 22:06（Asia/Shanghai，收盘后）** 从公开行情端点抓下来的**原始响应**，
用于在没有网络时复现数据层的解析口径与单位约定。**不要手工编辑内容**，只允许用脚本重新抓取。

| 文件 | 端点 | 说明 |
|---|---|---|
| `ths_kline_600000.js` | `https://d.10jqka.com.cn/v6/line/hs_600000/01/last.js` | 同花顺日线 JSONP，10 KB |
| `em_bkzj_industry.json` | `https://data.eastmoney.com/dataapi/bkzj/getbkzj?key=f62&code=m:90+t:2` | 东财行业板块资金流排名，31 KB |
| `em_fflow_600000.json` | `https://push2.eastmoney.com/api/qt/stock/fflow/kline/get?secid=1.600000&fields1=f1,f2,f3,f7&fields2=f51,f52,f53,f54,f55,f56&klt=101&lmt=10` | 东财个股资金流 |
| `sina_moneyflow_600000.js` | `https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssi_ssfx_flzjtj?daima=sh600000` | 新浪个股资金流兜底 |
| `tencent_quote_600000.txt` | `https://qt.gtimg.cn/q=sh600000` | 腾讯快照，**GBK 编码** |

## 抓取方式（复现命令）

```sh
UA='Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/126.0 Safari/537.36'
curl -sS --max-time 12 -A "$UA" -H 'Referer: https://stockpage.10jqka.com.cn/' \
  'https://d.10jqka.com.cn/v6/line/hs_600000/01/last.js' -o test/testdata/ths_kline_600000.js
# ……其余四个同上，URL 见上表
```

注意：抓取时**故意不带** `Accept-Encoding: gzip`，目的是留下可读的明文夹具；
线上客户端带 gzip 时出现的解压问题由 `internal/bootstrap/live_test.go`（`-tags live`）覆盖，不在这里测。

## 本次抓取确认的口径（写测试时以此为准）

1. **东财 fflow 分档**（DOMAIN 2.1）：`klines` 每根是 `f51,f52,f53,f54,f55,f56`，
   且实测满足 `f52(主力) == f55(大单) + f56(超大单)`、`f53(小单) + f54(中单) == -f52`。
2. **东财 bkzj**：`f62` 单位是**元**（电子 `9201537792` = 92.02 亿），不是万元/亿元。
3. **新浪 MoneyFlow**：`r0`~`r3` 是**成交额（买+卖）**，`r0 == r0_in + r0_out`；
   **净额必须用 `r*_in - r*_out`**。`docs/PLAN.md` §4 里"r0 超大 / r1 大 / r2 中 / r3 小（净额）"的说法不准确，
   按本文为准（已记入 `docs/QA.md` 缺口清单）。
4. **腾讯快照**：`~` 分隔，字段 [0]=市场标识、[1]=名称（GBK）、[2]=代码、[3]=现价、[4]=昨收、[5]=今开、[6]=成交量（**手**）。
   1 手 = 100 股，跨源比对时要乘 100。
5. **同花顺 K 线**：`data`（或 `bar`）是 `;` 分隔的字符串，单根 `日期(YYYYMMDD),开,高,低,收,量(股),额(元)`。

## 刷新夹具

```sh
./scripts/smoke.sh            # 先确认端点都通（失败只 WARN）
# 逐个重抓后再跑：go test ./test/ -run TestFixture -count=1
```
