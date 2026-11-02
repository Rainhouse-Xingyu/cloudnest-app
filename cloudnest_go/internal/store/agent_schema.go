package store

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

type agentTrigger struct{ name, table, event, body string }

func agentTriggers() []agentTrigger {
	const clear = "UPDATE agent_binding SET enrollment_id=NULL,enrollment_digest=NULL,enrollment_expires=NULL,credential_id=NULL,credential_digest=NULL,last_seen_at=NULL,client_version='' WHERE "
	return []agentTrigger{
		{"cn_agent_user_disable_v1", "user", "UPDATE", "BEGIN IF OLD.status=1 AND NEW.status<>1 THEN " + clear + "owner_id=OLD.id; END IF; END"},
		{"cn_agent_user_delete_v1", "user", "DELETE", clear + "owner_id=OLD.id"},
		{"cn_agent_device_change_v1", "device", "UPDATE", "BEGIN IF NEW.user_id<>OLD.user_id OR (OLD.status=1 AND NEW.status<>1) THEN " + clear + "device_id=OLD.id; END IF; END"},
		{"cn_agent_device_delete_v1", "device", "DELETE", clear + "device_id=OLD.id"},
	}
}

const agentTriggerProjection = "SELECT trigger_name,event_object_table AS table_name,event_manipulation AS event_name,action_timing AS timing,action_statement AS body FROM information_schema.triggers"

type triggerDefinition struct{ TriggerName, TableName, EventName, Timing, Body string }

func (row triggerDefinition) matches(s agentTrigger) bool {
	compact := func(v string) string { return strings.Join(strings.Fields(v), " ") }
	return row.TableName == s.table && row.EventName == s.event && row.Timing == "AFTER" && compact(row.Body) == compact(s.body)
}

func inspectTrigger(db *gorm.DB, s agentTrigger, migration bool) (bool, error) {
	var rows []triggerDefinition
	query := "CALL cn_agent_trigger_check_v1(?)"
	if migration {
		query = agentTriggerProjection + " WHERE trigger_schema=DATABASE() AND trigger_name=?"
	}
	err := db.Raw(query, s.name).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return false, err
	}
	if len(rows) != 1 || !rows[0].matches(s) {
		return false, fmt.Errorf("unexpected definition for agent trigger %s", s.name)
	}
	return true, nil
}

// Install only during explicit migrate. A failed DDL sequence can be retried;
// an existing trigger with unexpected behavior is never overwritten.
func installAgentTriggers(db *gorm.DB) error {
	for _, s := range agentTriggers() {
		found, err := inspectTrigger(db, s, true)
		if err != nil {
			return err
		}
		if found {
			continue
		}
		if err = db.Exec("CREATE TRIGGER " + s.name + " AFTER " + s.event + " ON `" + s.table + "` FOR EACH ROW " + s.body).Error; err != nil {
			return err
		}
	}
	// INFORMATION_SCHEMA.TRIGGERS depends on the caller's TRIGGER privilege.
	// This routine exposes only four named definitions from its own database.
	// Runtime credentials need EXECUTE on this one read-only definer routine.
	var schema string
	if err := db.Raw("SELECT DATABASE()").Scan(&schema).Error; err != nil {
		return err
	}
	if schema == "" {
		return fmt.Errorf("missing agent identity database")
	}
	var names []string
	for _, spec := range agentTriggers() {
		names = append(names, "'"+spec.name+"'")
	}
	body := agentTriggerProjection + fmt.Sprintf(" WHERE HEX(trigger_schema)='%X' AND trigger_name IN (%s) AND trigger_name=p_trigger_name", []byte(schema), strings.Join(names, ","))
	var routines []struct {
		Body     string
		Security string
	}
	err := db.Raw("SELECT routine_definition AS body,security_type AS security FROM information_schema.routines WHERE routine_schema=DATABASE() AND routine_name='cn_agent_trigger_check_v1' AND routine_type='PROCEDURE'").Scan(&routines).Error
	if err != nil {
		return err
	}
	if len(routines) == 0 {
		if err := db.Exec("CREATE PROCEDURE cn_agent_trigger_check_v1(IN p_trigger_name VARCHAR(64)) READS SQL DATA SQL SECURITY DEFINER " + body).Error; err != nil {
			return err
		}
	} else if len(routines) != 1 || routines[0].Security != "DEFINER" || strings.Join(strings.Fields(routines[0].Body), " ") != strings.Join(strings.Fields(body), " ") {
		return fmt.Errorf("unexpected agent schema verifier procedure")
	}
	return CheckAgentSchema(db)
}

func CheckAgentSchema(db *gorm.DB) error {
	var columns int
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='agent_binding' AND column_name IN ('device_id','owner_id','enrollment_id','enrollment_digest','enrollment_expires','credential_id','credential_digest','last_seen_at','client_version')").Scan(&columns).Error; err != nil {
		return err
	}
	if columns != 9 {
		return fmt.Errorf("agent identity schema missing; run explicit migrate")
	}
	for _, pair := range [][2]string{{"PRIMARY", "device_id"}, {"uq_agent_enrollment_id", "enrollment_id"}, {"uq_agent_credential_id", "credential_id"}} {
		var count int
		err := db.Raw("SELECT COUNT(*) FROM (SELECT index_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='agent_binding' AND index_name=? GROUP BY index_name HAVING COUNT(*)=1 AND MAX(non_unique)=0 AND MAX(column_name)=?) AS verified", pair[0], pair[1]).Scan(&count).Error
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("agent unique identity index missing: %s", pair[0])
		}
	}
	for _, s := range agentTriggers() {
		ok, err := inspectTrigger(db, s, false)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("agent trigger %s missing; run explicit migrate", s.name)
		}
	}
	return nil
}
