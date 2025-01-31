// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package traps

import (
	"fmt"
	"strings"

	"github.com/gosnmp/gosnmp"
)

const (
	sysUpTimeInstanceOID = "1.3.6.1.2.1.1.3.0"
	snmpTrapOID          = "1.3.6.1.6.3.1.1.4.1.0"
)

// FormatPacketToJSON converts an SNMP trap packet to a JSON-serializable object.
func FormatPacketToJSON(packet *SnmpPacket) (map[string]interface{}, error) {
	if packet.Content.Version == gosnmp.Version1 {
		return formatV1Trap(packet), nil
	}
	return formatTrap(packet)
}

// GetTags returns a list of tags associated to an SNMP trap packet.
func GetTags(packet *SnmpPacket) []string {
	return []string{
		fmt.Sprintf("snmp_version:%s", formatVersion(packet)),
		fmt.Sprintf("device_namespace:%s", GetNamespace()),
		fmt.Sprintf("snmp_device:%s", packet.Addr.IP.String()),
	}
}

func formatVersion(packet *SnmpPacket) string {
	switch packet.Content.Version {
	case gosnmp.Version3:
		return "3"
	case gosnmp.Version2c:
		return "2"
	case gosnmp.Version1:
		return "1"
	default:
		return "unknown"
	}
}

func formatV1Trap(packet *SnmpPacket) map[string]interface{} {
	data := make(map[string]interface{})
	data["uptime"] = uint32(packet.Content.Timestamp)
	enterpriseOid := normalizeOID(packet.Content.Enterprise)
	genericTrap := packet.Content.GenericTrap
	specificTrap := packet.Content.SpecificTrap
	var trapOID string
	if genericTrap == 6 {
		// Vendor-specific trap
		trapOID = fmt.Sprintf("%s.0.%d", enterpriseOid, specificTrap)
	} else {
		// Generic trap
		trapOID = fmt.Sprintf("%s.%d", genericTrapOid, genericTrap+1)
	}
	data["oid"] = trapOID
	data["enterprise_oid"] = enterpriseOid
	data["generic_trap"] = genericTrap
	data["specific_trap"] = specificTrap
	data["variables"] = parseVariables(packet.Content.Variables)

	return data
}

func formatTrap(packet *SnmpPacket) (map[string]interface{}, error) {
	/*
		An SNMP v2 or v3 trap packet consists in the following variables (PDUs):
		{sysUpTime.0, snmpTrapOID.0, additionalDataVariables...}
		See: https://tools.ietf.org/html/rfc3416#section-4.2.6
	*/
	variables := packet.Content.Variables
	if len(variables) < 2 {
		return nil, fmt.Errorf("expected at least 2 variables, got %d", len(variables))
	}

	data := make(map[string]interface{})

	uptime, err := parseSysUpTime(variables[0])
	if err != nil {
		return nil, err
	}
	data["uptime"] = uptime

	trapOID, err := parseSnmpTrapOID(variables[1])
	if err != nil {
		return nil, err
	}
	data["oid"] = trapOID

	data["variables"] = parseVariables(variables[2:])

	return data, nil
}

func normalizeOID(value string) string {
	// OIDs can be formatted as ".1.2.3..." ("absolute form") or "1.2.3..." ("relative form").
	// Convert everything to relative form, like we do in the Python check.
	return strings.TrimLeft(value, ".")
}

func parseSysUpTime(variable gosnmp.SnmpPDU) (uint32, error) {
	name := normalizeOID(variable.Name)
	if name != sysUpTimeInstanceOID {
		return 0, fmt.Errorf("expected OID %s, got %s", sysUpTimeInstanceOID, name)
	}

	value, ok := variable.Value.(uint32)
	if !ok {
		return 0, fmt.Errorf("expected uptime to be uint32 (got %v of type %T)", variable.Value, variable.Value)
	}

	return value, nil
}

func parseSnmpTrapOID(variable gosnmp.SnmpPDU) (string, error) {
	name := normalizeOID(variable.Name)
	if name != snmpTrapOID {
		return "", fmt.Errorf("expected OID %s, got %s", snmpTrapOID, name)
	}

	value := ""
	switch variable.Value.(type) {
	case string:
		value = variable.Value.(string)
	case []byte:
		value = string(variable.Value.([]byte))
	default:
		return "", fmt.Errorf("expected snmpTrapOID to be a string (got %v of type %T)", variable.Value, variable.Value)
	}

	return normalizeOID(value), nil
}

func parseVariables(variables []gosnmp.SnmpPDU) []map[string]interface{} {
	var parsedVariables []map[string]interface{}

	for _, variable := range variables {
		parsedVariable := make(map[string]interface{})
		parsedVariable["oid"] = normalizeOID(variable.Name)
		parsedVariable["type"] = formatType(variable)
		parsedVariable["value"] = formatValue(variable)
		parsedVariables = append(parsedVariables, parsedVariable)
	}

	return parsedVariables
}

func formatType(variable gosnmp.SnmpPDU) string {
	switch variable.Type {
	case gosnmp.Integer, gosnmp.Uinteger32:
		return "integer"
	case gosnmp.OctetString:
		return "string"
	case gosnmp.ObjectIdentifier:
		return "oid"
	case gosnmp.Counter32:
		return "counter32"
	case gosnmp.Counter64:
		return "counter64"
	case gosnmp.Gauge32:
		return "gauge32"
	default:
		return "other"
	}
}

func formatValue(variable gosnmp.SnmpPDU) interface{} {
	switch variable.Value.(type) {
	case []byte:
		return string(variable.Value.([]byte))
	default:
		return variable.Value
	}
}
