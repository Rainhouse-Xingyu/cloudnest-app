package service

import (
	"regexp"
	"strings"
)

type documentEvidenceClause struct {
	Kind       string `json:"kind"`
	Subject    string `json:"subject"`
	kind       *regexp.Regexp
	subject    *regexp.Regexp
	refundMode invoiceMode
}

type documentEvidence struct {
	kind, subject, refund bool
}

var invoiceEvidence = regexp.MustCompile(`发票|收据|报销凭证|红字票据|\b(?:(?:invoice|receipt)s?|credit notes?)\b`)
var contractEvidence = regexp.MustCompile(`合同|协议|契约|租约|\b(?:contract|agreement|lease)s?\b`)

type invoiceMode uint8

const (
	anyInvoice invoiceMode = iota
	purchaseInvoice
	refundInvoice
)

var refundEvidence = regexp.MustCompile(`红字发票|退款冲销|退货.{0,8}(?:发票|退款)|\b(?:credit note|refund invoice)\b`)

var purchaseEvidence = regexp.MustCompile(`购买|采购|报销|已付款|已支付|\b(?:purchase|paid|reimbursement)\b`)

var invoiceSubjects = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"computer", regexp.MustCompile(`笔记本|电脑|计算机|\b(?:laptop|notebook|computer|ultrabook)s?\b`)},
	{"printer", regexp.MustCompile(`打印机|印表机|\bprinters?\b`)},
	{"camera", regexp.MustCompile(`数码相机|照相机|摄影机|相机|\bcameras?\b`)},
	{"monitor", regexp.MustCompile(`显示器|\bmonitors?\b`)},
}

var contractSubjects = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"property_lease", regexp.MustCompile(`(?:房屋|住房|住宅|公寓|商铺|房产)(?:租赁|租约|出租)|\b(?:residential|apartment|property|commercial) lease\b`)},
}

func explicitDocumentEvidence(question string) *documentEvidenceClause {
	q := strings.ToLower(question)
	// Treat alternative names for the same document kind as one request.
	q = strings.NewReplacer("发票或收据", "发票", "收据或发票", "发票", "invoice or receipt", "invoice").Replace(q)
	if strings.ContainsAny(q, "或、") || strings.Contains(q, " or ") || strings.Contains(q, "而非") ||
		strings.Contains(q, "不含") || strings.Contains(q, "排除") || strings.Contains(q, "不要") {
		return nil
	}
	invoice := invoiceEvidence.MatchString(q) || (strings.Contains(q, "报销") && strings.Contains(q, "凭证"))
	if invoice && !contractEvidence.MatchString(q) {
		var clause *documentEvidenceClause
		mode := anyInvoice
		if refundEvidence.MatchString(q) {
			mode = refundInvoice
		} else if purchaseEvidence.MatchString(q) {
			mode = purchaseInvoice
		}
		for _, subject := range invoiceSubjects {
			if !subject.pattern.MatchString(q) {
				continue
			}
			if clause != nil {
				return nil
			}
			clause = &documentEvidenceClause{Kind: "invoice", Subject: subject.name, kind: invoiceEvidence, subject: subject.pattern, refundMode: mode}
		}
		return clause
	}
	if !invoice && (contractEvidence.MatchString(q) || strings.Contains(q, "租约")) {
		for _, subject := range contractSubjects {
			if subject.pattern.MatchString(q) {
				return &documentEvidenceClause{Kind: "contract", Subject: subject.name, kind: contractEvidence, subject: subject.pattern}
			}
		}
	}
	return nil
}

var deniedEvidence = regexp.MustCompile(`不(?:含|包含|包括|是|属于|涉及|提供|适用|开具)|未(?:开具|开票|提供)|并非|没有|无关|\b(?:not|no|without|excluding|excludes|does not include)\b`)
var anticipatedInvoice = regexp.MustCompile(`(?:才|将|会|再|后)(?:开具|提供|开出)|(?:将来|预计|拟).{0,12}(?:开具|提供)|\b(?:will|to be) (?:issue|issued|provide|provided)\b`)

func (e *documentEvidence) observe(rule *documentEvidenceClause, text string) {
	if rule == nil {
		return
	}
	for _, clause := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return strings.ContainsRune("。！？；，\n\r!?;,", r)
	}) {
		// Qualifiers apply within the clause so an unrelated disclaimer in a
		// later sentence does not erase affirmative evidence.
		if deniedEvidence.MatchString(clause) {
			continue
		}
		e.subject = e.subject || rule.subject.MatchString(clause)
		e.kind = e.kind || (rule.kind.MatchString(clause) &&
			(rule.Kind != "invoice" || !anticipatedInvoice.MatchString(clause)))
		e.refund = e.refund || refundEvidence.MatchString(clause)
	}
}

func (e documentEvidence) accepts(rule *documentEvidenceClause) bool {
	if rule == nil {
		return true
	}
	if !e.kind || !e.subject {
		return false
	}
	switch rule.refundMode {
	case purchaseInvoice:
		return !e.refund
	case refundInvoice:
		return e.refund
	default:
		return true
	}
}
