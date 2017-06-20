// +build ignore

package main

import (
	"fmt"
	"os"
	"strings"
	"text/template"
	"unicode"
)

type input struct {
	EntityName       string
	EntityNameSnake  string
	EntityNameDashes string
	EntityNameVar    string
}

func toSnakeCase(s string) string {
	var result []rune

	for i, roon := range s {
		if i > 0 && unicode.IsUpper(roon) {
			result = append(result, '_')
		}
		result = append(result, unicode.ToLower(roon))
	}

	return string(result)
}

func toDashes(s string) string {
	return strings.Replace(toSnakeCase(s), "_", "-", -1)
}

func toVarName(s string) string {
	var runes []rune

	runes = append(runes, unicode.ToLower(rune(s[0])))
	runes = append(runes, []rune(s[1:len(s)])...)

	return string(runes)
}

func main() {
	codeTemplate, err := template.ParseFiles("codegen/get_by_guid.go.template")
	if err != nil {
		panic(err)
	}

	templateInput := input{
		EntityName:       os.Args[1],
		EntityNameSnake:  toSnakeCase(os.Args[1]),
		EntityNameDashes: toDashes(os.Args[1]),
		EntityNameVar:    toVarName(os.Args[1]),
	}

	codeFilename := fmt.Sprintf("get_%s.go", templateInput.EntityNameSnake)
	codeFile, err := os.Create(codeFilename)
	defer codeFile.Close()
	if err != nil {
		panic(err)
	}

	fmt.Printf("writing %s\n", codeFilename)

	err = codeTemplate.Execute(codeFile, templateInput)
	if err != nil {
		panic(err)
	}

	testTemplate, err := template.ParseFiles("codegen/get_by_guid_test.go.template")
	if err != nil {
		panic(err)
	}

	testFilename := fmt.Sprintf("get_%s_test.go", templateInput.EntityNameSnake)
	testFile, err := os.Create(testFilename)
	defer testFile.Close()
	if err != nil {
		panic(err)
	}

	fmt.Printf("writing %s\n", testFilename)

	err = testTemplate.Execute(testFile, templateInput)
	if err != nil {
		panic(err)
	}
}
