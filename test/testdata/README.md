# test/testdata：真实响应夹具

这些文件是 **2026-11-12（Asia/Shanghai，盘中）** 从公开行情端点抓下来的**原始响应**，
用于在没有网络时复现数据层的解析口径与单位约定。**不要手工编辑内容**，只允许用脚本重新抓取。

数据层已经**完全移除东财**，夹具只保留同花顺 / 腾讯 / 新浪三家。

| 文件 | 端点 | 说明 |
|---|---|---|
| `ths_kline_600000.js` | `https://d.10jqka.com.cn/v6/line/hs_600000/01/last.js` | 同花顺日线 JSONP，10 KB |
| `ths_limit_up_pool.json` | `https://data.10jqka.com.cn/dataapi/limit_up/limit_up_pool?...` | 同花顺涨停池（含 limit_up_count / limit_down_count 汇总） |
| `sina_fund_flow_600000.json` | `.../MoneyFlow.ssi_ssfx_flzjtj?daima=sh600000` | 新浪个股当日资金流（r0~r3 的 in/out） |
| `sina_moneyflow_600000.js` | 同上（早期样本） | 新浪个股资金流的历史样本，用于交叉校验 |
| `tencent_quote_600000.txt` | `https://qt.gtimg.cn/q=sh600000` | 腾讯快照，**GBK 编码** |

## 抓取方式（复现命令）

```sh
UA='Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/131.0 Safari/537.36'
curl -sS --max-time 12 -A "$UA" -H 'Referer: https://stockpage.10jqka.com.cn/' \
  'https://d.10jqka.com.cn/v6/line/hs_600000/01/last.js' -o test/testdata/ths_kline_600000.js
# 同花顺 dataapi 必须带浏览器 UA + data.10jqka.com.cn 的 Referer，否则被 Nginx 403
curl -sS --max-time 12 -A "$UA" -H 'Referer: https://data.10jqka.com.cn/market/longhu/' \
  'https://data.10jqka.com.cn/dataapi/limit_up/limit_up_pool?page=1&limit=200&field=10,9001,9002,330323,330324,330325,330329,199112,133971,133970,1968584&filter=HS,GEM2STAR&order_field=330324&order_type=0' \
  -o test/testdata/ths_limit_up_pool.json
# ……其余同上，URL 见上表与 internal/datasource/testdata 里更完整的样本集
```

注意：抓取时**故意不带** `Accept-Encoding: gzip`，目的是留下可读的明文夹具；
线上客户端带 gzip 时出现的解压问题由 `internal/bootstrap/live_test.go`（`-tags live`）覆盖，不在这里测。

## 本次抓取确认的口径（写测试时以此为准）

1. **新浪个股东财等价口径**：`r0`~`r3` 是**成交额（买+卖）**，`r0 == r0_in + r0_out`；
   **净额必须用 `r*_in - r*_out`**。
   主力净额（与旧东财同名口径等价）= `r0_net + r1_net`（超大单 + 大单）；
   四档净额之和等于 `netamount`，可以用它做交叉校验。
2. **新浪全市场快照**（`Market_Center.getHQNodeData?node=hs_a`）：
   `trade`/`settlement`/`changepercent`/`amount` 会**混用数字与字符串**，解析时必须先归一成 any 再取数。
   涨跌家数按 `changepercent` 正/负/零分桶；停牌股（`trade=0` 且 `settlement>0`）不计入平盘。
3. **新浪板块目录**（`Market_Center.getHQNodes`）：节点树首项是标题，叶子是
   `[名称, 说明, category]` 三元组。只有「新浪行业」（`new_*`）与「概念板块」（`gn_*`）两支
   有资金流数据，其余（申万 `sw*`、证监会 `hangye_*`）不要暴露给用户。
   板的 code 就是 category，旧东财 `BK1201` 已下线。
4. **腾讯快照**：`~` 分隔，字段 [0]=市场标识、[1]=名称（GBK）、[2]=代码、[3]=现价、[4]=昨收、[5]=今开、[6]=成交量（**手**）。
   1 手 = 100 股，跨源比对时要乘 100。
5. **腾讯 K 线**：`param=<symbol>,<period>,,,<count>[,qfq|hfq]`，
   响应键分别是 `qfqday` / `hfqday` / `day`（周月同理）。**后复权可用**，不要再用「只有东财能给 hfq」这个前提。
6. **同花顺 K 线**：`data`（或 `bar`）是 `;` 分隔的字符串，单根 `日期(YYYYMMDD),开,高,低,收,量(股),额(元)`。
7. **同花顺涨停池**：`limit_up_count.today.{num,history_num,rate,open_num}` =
   涨停数 / 曾涨停数 / 封板率 / 炸板数；`limit_down_count.today.num` 是跌停数。
8. **同花顺龙虎榜**（`/ifmarket/lhbtable/report/<date>/1`）是 **GBK 编码 HTML 表格**，
   金额是 `1.17亿 / 5830.19万` 这种中文量级，必须换算成元。该表没有上榜原因与买卖拆分。

## 刷新夹具

```sh
./scripts/smoke.sh            # 先确认端点都通（失败只 WARN）
# 逐个重抓后再跑：go test ./test/ -run TestFixture -count=1
```
