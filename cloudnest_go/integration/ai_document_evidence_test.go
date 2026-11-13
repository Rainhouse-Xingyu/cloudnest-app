//go:build integration && aisearch

package integration

import "testing"

func TestAIHybridRequiresEvidenceForExplicitDocumentSubject(t *testing.T) {
	f, _ := indexFixture(t)
	add := func(name, text string) int64 {
		file := f.upload(t, f.alice, name, []byte(text))
		lexicalIndexFixture(t, f, file, text)
		return file.ID
	}
	computer := add("expense.txt", "电脑购买发票\n商品：笔记本电脑。付款状态：已支付。")
	refund := add("return.txt", "电脑退货红字发票\n商品为笔记本电脑。退款冲销凭证。")
	add("quote.txt", "笔记本电脑报价单\n预计采购电脑，成交后才会开具发票。")
	add("contract.txt", "电脑采购合同\n验收后才提供发票，不是已付款的发票或收据。")
	add("打印机发票.txt", "相机采购发票，商品为数码相机。不含打印机。")
	add("lease.txt", "软件维护服务合同，双方已经签署。")
	add("房屋租赁合同.txt", "讨论维护服务协议。本文不涉及房屋租赁。")

	check := func(question string, want int64) {
		t.Helper()
		got := hybridQuery(t, f, f.alice, question)
		if want == 0 && len(got.Items) == 0 {
			return
		}
		if want == 0 || len(got.Items) != 1 || got.Items[0].ID != want {
			t.Fatalf("query %q: got %+v, want only %d", question, got, want)
		}
	}
	check("购买笔记本电脑的费用发票，用于支出报销", computer)
	check("电脑退货开出的红字发票", refund)
	check("已支付的打印机购买发票", 0)
	check("已签署的房屋租赁合同", 0)
	lease := add("housing.txt", "房屋租赁合同。出租方与承租方约定押金和租金。双方已签署。")
	check("已签署的房屋租赁合同", lease)
	printer := add("hardware.txt", "办公打印机购买发票。商品为打印机，款项已支付。")
	check("已支付的打印机购买发票", printer)
}
