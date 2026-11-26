package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

func writeOrderCSV(w http.ResponseWriter, orders []orderHTTPResult) {
	records := make([][]string, 0, len(orders)+1)
	records = append(records, []string{"订单编号", "原订单 ID", "订单来源", "业务状态", "来源金额（元）", "归属产品",
		"业务时间", "版本", "客户手机号", "推荐人手机号", "销售人员手机号"})
	for _, order := range orders {
		var display map[string]json.RawMessage
		if err := json.Unmarshal(order["display"], &display); err != nil {
			respond(w, nil, err)
			return
		}
		var amount *int64
		if raw, ok := order["amountMinor"]; ok {
			if err := json.Unmarshal(raw, &amount); err != nil {
				respond(w, nil, err)
				return
			}
		}
		amountText := ""
		if amount != nil {
			amountText = orderCSVAmount(*amount)
		}
		number := orderText(order["orderNo"])
		if number == "" {
			number = orderText(order["orderId"])
		}
		records = append(records, []string{safeOrderCSVText(number), safeOrderCSVText(orderText(order["orderId"])),
			safeOrderCSVText(orderText(order["source"])), safeOrderCSVText(orderText(order["kind"])),
			amountText, safeOrderCSVText(orderText(order["productName"])), safeOrderCSVText(orderText(order["occurredAt"])),
			safeOrderCSVText(orderText(order["version"])), safeOrderCSVText(orderText(display["customerPhone"])),
			safeOrderCSVText(orderText(display["referrerPhone"])), safeOrderCSVText(orderText(display["sellerPhone"]))})
	}
	var body bytes.Buffer
	body.WriteString("\uFEFF")
	writer := csv.NewWriter(&body)
	writer.UseCRLF = true
	if err := writer.WriteAll(records); err != nil {
		respond(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="finance-orders.csv"`)
	_, _ = w.Write(body.Bytes())
}

func orderCSVAmount(minor int64) string {
	digits := strconv.FormatInt(minor, 10)
	sign := ""
	if minor < 0 {
		sign, digits = "-", digits[1:]
	}
	if len(digits) < 3 {
		digits = strings.Repeat("0", 3-len(digits)) + digits
	}
	return sign + digits[:len(digits)-2] + "." + digits[len(digits)-2:]
}

func safeOrderCSVText(value string) string {
	trimmed := strings.TrimLeftFunc(value, unicode.IsSpace)
	if strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") ||
		(trimmed != "" && strings.ContainsRune("=+-@＝＋－＠", []rune(trimmed)[0])) {
		return "'" + value
	}
	return value
}
