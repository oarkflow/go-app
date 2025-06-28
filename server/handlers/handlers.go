package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/oarkflow/squealx"

	"github.com/oarkflow/dag/server/pkg/config"
)

var (
	DefaultDB    *squealx.DB
	ModelsMap    map[string]config.Model
	AuthCfg      config.Auth
	ProvidersMap map[string]*squealx.DB
)

func Create(c *fiber.Ctx) error {
	modelName := extractModelName(c)
	m, ok := ModelsMap[modelName]
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": "model not found"})
	}
	var payload map[string]any
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid JSON"})
	}
	table := m.Table
	if table == "" {
		table = toName(modelName)
	}
	// Build column lists and a parameter map for named placeholders.
	var cols []string
	paramMap := make(map[string]any)
	for _, field := range m.Fields {
		if v, exists := payload[field.Name]; exists {
			cols = append(cols, field.Name)
			paramMap[field.Name] = v
		}
	}
	if len(cols) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "no valid fields provided"})
	}
	// Build query using named parameters.
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(cols, ", "),
		":"+strings.Join(cols, ", :"))
	db := getModelDB(m)
	res, err := db.Exec(query, paramMap)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	id, _ := res.LastInsertId()
	return c.JSON(fiber.Map{"id": id})
}

func Read(c *fiber.Ctx) error {
	modelName := extractModelName(c)
	id := c.Params("id")
	m, ok := ModelsMap[modelName]
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": "model not found"})
	}
	table := m.Table
	if table == "" {
		table = toName(modelName)
	}
	db := getModelDB(m)
	query := fmt.Sprintf("SELECT * FROM %s WHERE id = :id", table)
	var result map[string]any
	err := db.Select(&result, query, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.Status(404).JSON(fiber.Map{"error": "record not found"})
		}
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"data": result})
}

func Update(c *fiber.Ctx) error {
	modelName := extractModelName(c)
	id := c.Params("id")
	m, ok := ModelsMap[modelName]
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": "model not found"})
	}
	table := m.Table
	if table == "" {
		table = toName(modelName)
	}
	var payload map[string]any
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid JSON"})
	}
	setClauses := []string{}
	paramMap := make(map[string]any)
	for _, field := range m.Fields {
		if v, exists := payload[field.Name]; exists {
			setClauses = append(setClauses, fmt.Sprintf("%s = :%s", field.Name, field.Name))
			paramMap[field.Name] = v
		}
	}
	if len(setClauses) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "no valid fields to update"})
	}
	paramMap["id"] = id
	query := fmt.Sprintf("UPDATE %s SET %s WHERE id = :id", table, strings.Join(setClauses, ", "))
	db := getModelDB(m) // using getModelDB instead of DefaultDB directly.
	_, err := db.Exec(query, paramMap)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"success": true})
}

func Delete(c *fiber.Ctx) error {
	modelName := extractModelName(c)
	id := c.Params("id")
	m, ok := ModelsMap[modelName]
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": "model not found"})
	}
	table := m.Table
	if table == "" {
		table = toName(modelName)
	}
	paramMap := map[string]any{"id": id}
	var err error
	softDelete := false
	var softField string
	for _, field := range m.Fields {
		if field.SoftDelete {
			softDelete = true
			softField = field.Name
			break
		}
	}
	db := getModelDB(m) // use the proper DB.
	if softDelete {
		query := fmt.Sprintf("UPDATE %s SET %s = :soft WHERE id = :id", table, softField)
		paramMap["soft"] = true
		_, err = db.Exec(query, paramMap)
	} else {
		query := fmt.Sprintf("DELETE FROM %s WHERE id = :id", table)
		_, err = db.Exec(query, paramMap)
	}
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error(), "action": "delete"})
	}
	return c.JSON(fiber.Map{"success": true})
}

func List(c *fiber.Ctx) error {
	modelName := extractModelName(c)
	m, ok := ModelsMap[modelName]
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": "model not found"})
	}
	table := m.Table
	if table == "" {
		table = toName(modelName)
	}
	query := fmt.Sprintf("SELECT * FROM %s", table)
	db := getModelDB(m) // use getModelDB instead of DefaultDB.
	rows, err := db.Query(query, nil)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error(), "action": "query list"})
	}
	defer rows.Close()
	var results []map[string]any
	cols, err := rows.Columns()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error(), "action": "get columns list"})
	}
	for rows.Next() {
		columns := make([]any, len(cols))
		columnPointers := make([]any, len(cols))
		for i := range columns {
			columnPointers[i] = &columns[i]
		}
		if err := rows.Scan(columnPointers...); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error(), "action": "scan row list"})
		}
		record := make(map[string]any)
		for i, colName := range cols {
			val := columnPointers[i].(*any)
			record[colName] = *val
		}
		results = append(results, record)
	}
	return c.JSON(fiber.Map{"records": results})
}

func singularToPlural(word string) string {
	if len(word) == 0 {
		return word
	}
	if strings.HasSuffix(word, "s") {
		return word
	}
	if strings.HasSuffix(word, "y") {
		return word[:len(word)-1] + "ies"
	}
	return word + "s"
}

func pluralToSingular(word string) string {
	if len(word) == 0 {
		return word
	}
	if strings.HasSuffix(word, "ies") {
		return word[:len(word)-3] + "y"
	}
	if strings.HasSuffix(word, "s") {
		return word[:len(word)-1]
	}
	return word
}

func ToSlug(word string) string {
	slug := strings.ReplaceAll(strings.ToLower(word), "_", "-")
	return singularToPlural(slug)
}

func toName(word string) string {
	name := strings.ReplaceAll(strings.ToLower(word), "-", "_")
	return singularToPlural(name)
}

func extractModelName(c *fiber.Ctx) string {
	modelName := c.Params("model")
	if modelName == "" {
		if val := c.Locals("model"); val != nil {
			modelName = val.(string)
		}
	}
	return modelName
}

func getModelDB(m config.Model) *squealx.DB {
	if m.Provider != "" {
		if db, ok := ProvidersMap[m.Provider]; ok {
			return db
		}
	}
	return DefaultDB
}
