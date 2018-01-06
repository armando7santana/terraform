package format

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform/config"
	"github.com/hashicorp/terraform/config/configschema"
	"github.com/hashicorp/terraform/diffs"
	"github.com/hashicorp/terraform/terraform"
	"github.com/mitchellh/colorstring"
	"github.com/zclconf/go-cty/cty"
)

// ResourceChange returns a string representation of a change to a particular
// resource, for inclusion in user-facing plan output.
//
// The resource schema must be provided along with the change so that the
// formatted change can reflect the configuration structure for the associated
// resource.
//
// If "color" is non-nil, it will be used to color the result. Otherwise,
// no color codes will be included.
func ResourceChange(
	addr *terraform.ResourceAddress,
	change *diffs.Change,
	schema *configschema.Block,
	color *colorstring.Colorize,
) string {
	var buf bytes.Buffer

	if color == nil {
		color = &colorstring.Colorize{
			Colors:  colorstring.DefaultColors,
			Disable: true,
			Reset:   false,
		}
	}

	buf.WriteString(color.Color("[reset]"))

	switch change.Action {
	case diffs.Create:
		buf.WriteString(color.Color("[green]  +[reset] "))
	case diffs.Read:
		buf.WriteString(color.Color("[cyan] <=[reset] "))
	case diffs.Update:
		buf.WriteString(color.Color("[yellow]  ~[reset] "))
	case diffs.Replace:
		buf.WriteString(color.Color("[red]-[reset]/[green]+[reset] "))
	case diffs.Delete:
		buf.WriteString(color.Color("[red]  -[reset] "))
	default:
		// should never happen, since the above is exhaustive
		buf.WriteString(color.Color("??? "))
	}

	switch addr.Mode {
	case config.ManagedResourceMode:
		buf.WriteString(color.Color(fmt.Sprintf(
			"resource [bold]%q[reset] [bold]%q[reset] {",
			addr.Type,
			addr.Name,
		)))
	case config.DataResourceMode:
		buf.WriteString(color.Color(fmt.Sprintf(
			"data [bold]%q[reset] [bold]%q[reset] {",
			addr.Type,
			addr.Name,
		)))
	default:
		// should never happen, since the above is exhaustive
		buf.WriteString(addr.String())
	}

	if change.Action == diffs.Replace {
		buf.WriteString(" [bold]# new resource required[bold]")
	}
	buf.WriteString("\n")

	writeBlockBodyDiff(schema, change.Old, change.New, &buf, 6, color)

	buf.WriteString("    }\n")

	return buf.String()
}

func writeBlockBodyDiff(schema *configschema.Block, old, new cty.Value, buf *bytes.Buffer, indent int, color *colorstring.Colorize) {
	{
		attrNames := make([]string, len(schema.Attributes))
		attrNameLen := 0
		for name := range schema.Attributes {
			attrNames = append(attrNames, name)
			if len(name) > attrNameLen {
				attrNameLen = len(name)
			}
		}
		sort.Strings(attrNames)

		for _, name := range attrNames {
			attrS := schema.Attributes[name]
			oldVal := old.GetAttr(name)
			newVal := new.GetAttr(name)
			writeAttrDiff(name, attrS, oldVal, newVal, buf, attrNameLen, indent, color)
		}
	}

	// TODO: Nested blocks
}

func writeAttrDiff(name string, attrS *configschema.Attribute, old, new cty.Value, buf *bytes.Buffer, nameLen, indent int, color *colorstring.Colorize) {
	if new.RawEquals(old) {
		// Don't print anything for unchanged attributes
		return
	}

	switch {
	case old.IsNull():
		buf.WriteString(color.Color("[green]+[reset] "))
		buf.WriteString(strings.Repeat(" ", indent))
		buf.WriteString(name)
		buf.WriteString(strings.Repeat(" ", nameLen-len(name)))
		buf.WriteString(" = ")
		if attrS.Sensitive {
			buf.WriteString("(sensitive value)")
		} else {
			writeValue(new, buf, indent, color)
		}
		buf.WriteString("\n")
	case new.IsNull():
		buf.WriteString(color.Color("[red]-[reset] "))
		buf.WriteString(strings.Repeat(" ", indent))
		buf.WriteString(name)
		buf.WriteString(strings.Repeat(" ", nameLen-len(name)))
		buf.WriteString(" = ")
		if attrS.Sensitive {
			buf.WriteString("(sensitive value)")
		} else {
			writeValue(new, buf, indent, color)
		}
		buf.WriteString("\n")
	default:
		buf.WriteString(color.Color("[yellow]~[reset] "))
		buf.WriteString(strings.Repeat(" ", indent))
		buf.WriteString(name)
		buf.WriteString(strings.Repeat(" ", nameLen-len(name)))
		buf.WriteString(" = ")
		if attrS.Sensitive {
			buf.WriteString("(sensitive value)")
		} else {
			writeValueDiff(old, new, buf, indent, color)
		}
		buf.WriteString("\n")
	}

}

func writeValue(val cty.Value, buf *bytes.Buffer, indent int, color *colorstring.Colorize) {
	if !val.IsKnown() {
		buf.WriteString("(not yet known)")
		return
	}
	if val.IsNull() {
		buf.WriteString("null")
		return
	}

	ty := val.Type()

	switch {
	case ty.IsPrimitiveType():
		switch ty {
		case cty.String:
			fmt.Fprintf(buf, "%q", val.AsString())
		case cty.Bool:
			if val.True() {
				buf.WriteString("true")
			} else {
				buf.WriteString("false")
			}
		case cty.Number:
			bf := val.AsBigFloat()
			buf.WriteString(bf.Text('f', -1))
		default:
			// should never happen, since the above is exhaustive
			fmt.Fprintf(buf, "%#v", val)
		}
	case ty.IsListType() || ty.IsSetType() || ty.IsTupleType():
		buf.WriteString("[\n")

		it := val.ElementIterator()
		for it.Next() {
			_, val := it.Element()
			indent := indent + 2
			buf.WriteString(strings.Repeat(" ", indent))
			writeValue(val, buf, indent, color)
			buf.WriteString(",\n")
		}

		buf.WriteString(strings.Repeat(" ", indent))
		buf.WriteString("]")
	case ty.IsMapType():
		buf.WriteString("{\n")

		it := val.ElementIterator()
		for it.Next() {
			key, val := it.Element()
			indent := indent + 2
			buf.WriteString(strings.Repeat(" ", indent))
			writeValue(key, buf, indent, color)
			buf.WriteString(" = ")
			writeValue(val, buf, indent, color)
			buf.WriteString("\n")
		}

		buf.WriteString(strings.Repeat(" ", indent))
		buf.WriteString("}")
	case ty.IsObjectType():
		buf.WriteString("{\n")

		atys := ty.AttributeTypes()
		attrNames := make([]string, 0, len(atys))
		nameLen := 0
		for attrName := range atys {
			attrNames = append(attrNames, attrName)
			if len(attrName) > nameLen {
				nameLen = len(attrName)
			}
		}
		sort.Strings(attrNames)

		for _, attrName := range attrNames {
			indent := indent + 2
			val := val.GetAttr(attrName)
			buf.WriteString(strings.Repeat(" ", indent))
			buf.WriteString(attrName)
			buf.WriteString(strings.Repeat(" ", nameLen-len(attrName)))
			buf.WriteString(" = ")
			writeValue(val, buf, indent, color)
			buf.WriteString("\n")
		}

		buf.WriteString(strings.Repeat(" ", indent))
		buf.WriteString("}")
	}
}

func writeValueDiff(old, new cty.Value, buf *bytes.Buffer, indent int, color *colorstring.Colorize) {
	// TODO: Add specialized diff implementations for:
	//   - collections (adding/removing/changing individual elements)
	//   - multi-line strings (line-based diff)

	writeValue(old, buf, indent, color)
	buf.WriteString(" -> ")
	writeValue(new, buf, indent, color)
}
