package format

import (
	"bufio"
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

	p := blockBodyDiffPrinter{
		buf:   &buf,
		color: color,
	}
	p.writeBlockBodyDiff(schema, change.Old, change.New, 6)

	buf.WriteString("    }\n")

	return buf.String()
}

type blockBodyDiffPrinter struct {
	buf   *bytes.Buffer
	color *colorstring.Colorize
}

func (p *blockBodyDiffPrinter) writeBlockBodyDiff(schema *configschema.Block, old, new cty.Value, indent int) {
	{
		attrNames := make([]string, 0, len(schema.Attributes))
		attrNameLen := 0
		for name := range schema.Attributes {
			oldVal := ctyGetAttrMaybeNull(old, name)
			newVal := ctyGetAttrMaybeNull(new, name)
			if oldVal.RawEquals(newVal) {
				// Skip attributes that have no change
				// (we do this early here so that we'll do our value alignment
				// based on the longest attribute name that has a change, rather
				// than the longest attribute name in the full set.)
				continue
			}

			attrNames = append(attrNames, name)
			if len(name) > attrNameLen {
				attrNameLen = len(name)
			}
		}
		sort.Strings(attrNames)

		for _, name := range attrNames {
			attrS := schema.Attributes[name]
			oldVal := ctyGetAttrMaybeNull(old, name)
			newVal := ctyGetAttrMaybeNull(new, name)

			p.writeAttrDiff(name, attrS, oldVal, newVal, attrNameLen, indent)
		}
	}

	// TODO: Nested blocks
}

func (p *blockBodyDiffPrinter) writeAttrDiff(name string, attrS *configschema.Attribute, old, new cty.Value, nameLen, indent int) {
	if new.RawEquals(old) {
		// Don't print anything for unchanged attributes
		return
	}

	p.buf.WriteString(strings.Repeat(" ", indent))
	switch {
	case old.IsNull():
		p.buf.WriteString(p.color.Color("[green]+[reset] "))
	case new.IsNull():
		p.buf.WriteString(p.color.Color("[red]-[reset] "))
	default:
		p.buf.WriteString(p.color.Color("[yellow]~[reset] "))
	}

	p.buf.WriteString(p.color.Color("[bold]"))
	p.buf.WriteString(name)
	p.buf.WriteString(p.color.Color("[reset]"))
	p.buf.WriteString(strings.Repeat(" ", nameLen-len(name)))
	p.buf.WriteString(" = ")

	if attrS.Sensitive {
		p.buf.WriteString("(sensitive value)")
	} else {
		switch {
		case old.IsNull():
			p.writeValue(new, indent+2)
		default:
			// We show new even if it is null to emphasize the fact
			// that it is being unset, since otherwise it is easy to
			// misunderstand that the value is still set to the old value.
			p.writeValueDiff(old, new, indent+2)
		}
	}

	p.buf.WriteString("\n")

}

func (p *blockBodyDiffPrinter) writeValue(val cty.Value, indent int) {
	if !val.IsKnown() {
		p.buf.WriteString("(not yet known)")
		return
	}
	if val.IsNull() {
		p.buf.WriteString("null")
		return
	}

	ty := val.Type()

	switch {
	case ty.IsPrimitiveType():
		switch ty {
		case cty.String:
			fmt.Fprintf(p.buf, "%q", val.AsString())
		case cty.Bool:
			if val.True() {
				p.buf.WriteString("true")
			} else {
				p.buf.WriteString("false")
			}
		case cty.Number:
			bf := val.AsBigFloat()
			p.buf.WriteString(bf.Text('f', -1))
		default:
			// should never happen, since the above is exhaustive
			fmt.Fprintf(p.buf, "%#v", val)
		}
	case ty.IsListType() || ty.IsSetType() || ty.IsTupleType():
		p.buf.WriteString("[\n")

		it := val.ElementIterator()
		for it.Next() {
			_, val := it.Element()
			indent := indent + 4 // we add four here to consume space where the diff icon would go
			p.buf.WriteString(strings.Repeat(" ", indent))
			p.writeValue(val, indent)
			p.buf.WriteString(",\n")
		}

		p.buf.WriteString(strings.Repeat(" ", indent))
		p.buf.WriteString("]")
	case ty.IsMapType():
		p.buf.WriteString("{\n")

		it := val.ElementIterator()
		for it.Next() {
			key, val := it.Element()
			indent := indent + 4 // we add four here to consume space where the diff icon would go
			p.buf.WriteString(strings.Repeat(" ", indent))
			p.writeValue(key, indent)
			p.buf.WriteString(" = ")
			p.writeValue(val, indent)
			p.buf.WriteString("\n")
		}

		p.buf.WriteString(strings.Repeat(" ", indent))
		p.buf.WriteString("}")
	case ty.IsObjectType():
		p.buf.WriteString("{\n")

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
			indent := indent + 4 // we add four here to consume space where the diff icon would go
			val := val.GetAttr(attrName)
			p.buf.WriteString(strings.Repeat(" ", indent))
			p.buf.WriteString(attrName)
			p.buf.WriteString(strings.Repeat(" ", nameLen-len(attrName)))
			p.buf.WriteString(" = ")
			p.writeValue(val, indent)
			p.buf.WriteString("\n")
		}

		p.buf.WriteString(strings.Repeat(" ", indent))
		p.buf.WriteString("}")
	}
}

func (p *blockBodyDiffPrinter) writeValueDiff(old, new cty.Value, indent int) {
	ty := old.Type()

	// We have some specialized diff implementations for certain complex
	// values where it's useful to see a visualization of the diff of
	// the nested elements rather than just showing the entire old and
	// new values verbatim.
	// However, these specialized implementations can apply only if both
	// values are known and non-null.
	if old.IsKnown() && new.IsKnown() && !old.IsNull() && !new.IsNull() {
		switch {
		// TODO: list diffs using longest-common-subsequence matching algorithm
		// TODO: map diffs showing changes on a per-key basis
		// TODO: multi-line string diffs showing lines added/removed using longest-common-subsequence

		case ty == cty.String:
			// We only have special behavior for multi-line strings here
			oldS := old.AsString()
			newS := new.AsString()
			if strings.Index(oldS, "\n") < 0 && strings.Index(newS, "\n") < 0 {
				break
			}

			p.buf.WriteString("<<~EOT\n")

			var oldLines, newLines []cty.Value
			{
				r := strings.NewReader(oldS)
				sc := bufio.NewScanner(r)
				for sc.Scan() {
					oldLines = append(oldLines, cty.StringVal(sc.Text()))
				}
			}
			{
				r := strings.NewReader(newS)
				sc := bufio.NewScanner(r)
				for sc.Scan() {
					newLines = append(newLines, cty.StringVal(sc.Text()))
				}
			}

			lcsLines := diffs.LongestCommonSubsequence(oldLines, newLines)
			var oldI, newI, lcsI int
			for oldI < len(oldLines) || newI < len(newLines) || lcsI < len(lcsLines) {
				for oldI < len(oldLines) && (lcsI >= len(lcsLines) || !oldLines[oldI].RawEquals(lcsLines[lcsI])) {
					line := oldLines[oldI].AsString()
					p.buf.WriteString(strings.Repeat(" ", indent+2))
					p.buf.WriteString(p.color.Color("[red]-[reset] "))
					p.buf.WriteString(line)
					p.buf.WriteString("\n")
					oldI++
				}
				for newI < len(newLines) && (lcsI >= len(lcsLines) || !newLines[newI].RawEquals(lcsLines[lcsI])) {
					line := newLines[newI].AsString()
					p.buf.WriteString(strings.Repeat(" ", indent+2))
					p.buf.WriteString(p.color.Color("[green]+[reset] "))
					p.buf.WriteString(line)
					p.buf.WriteString("\n")
					newI++
				}
				if lcsI < len(lcsLines) {
					line := lcsLines[lcsI].AsString()
					p.buf.WriteString(strings.Repeat(" ", indent+4)) // +4 here because there's no symbol
					p.buf.WriteString(line)
					p.buf.WriteString("\n")
					// All of our indexes advance together now, since the line
					// is common to all three sequences.
					lcsI++
					oldI++
					newI++
				}
			}

			p.buf.WriteString(strings.Repeat(" ", indent)) // +4 here because there's no symbol
			p.buf.WriteString("EOT")

			return

		case ty.IsSetType():
			p.buf.WriteString("[\n")

			var addedVals, removedVals, allVals []cty.Value
			for it := old.ElementIterator(); it.Next(); {
				_, val := it.Element()
				allVals = append(allVals, val)
				if new.HasElement(val).False() {
					removedVals = append(removedVals, val)
				}
			}
			for it := new.ElementIterator(); it.Next(); {
				_, val := it.Element()
				allVals = append(allVals, val)
				if old.HasElement(val).False() {
					addedVals = append(addedVals, val)
				}
			}

			var all, added, removed cty.Value
			if len(allVals) > 0 {
				all = cty.SetVal(allVals)
			} else {
				all = cty.SetValEmpty(ty.ElementType())
			}
			if len(addedVals) > 0 {
				added = cty.SetVal(addedVals)
			} else {
				added = cty.SetValEmpty(ty.ElementType())
			}
			if len(removedVals) > 0 {
				removed = cty.SetVal(removedVals)
			} else {
				removed = cty.SetValEmpty(ty.ElementType())
			}

			for it := all.ElementIterator(); it.Next(); {
				_, val := it.Element()

				p.buf.WriteString(strings.Repeat(" ", indent+2))
				switch {
				case added.HasElement(val).True():
					p.buf.WriteString(p.color.Color("[green]+[reset] "))
				case removed.HasElement(val).True():
					p.buf.WriteString(p.color.Color("[red]-[reset] "))
				default:
					p.buf.WriteString("  ")
				}

				p.writeValue(val, indent+4)
				p.buf.WriteString(",\n")
			}

			p.buf.WriteString(strings.Repeat(" ", indent))
			p.buf.WriteString("]")
			return
		}
	}

	// In all other cases, we just show the new and old values as-is
	p.writeValue(old, indent)
	p.buf.WriteString(" -> ")
	p.writeValue(new, indent)
}

func ctyGetAttrMaybeNull(val cty.Value, name string) cty.Value {
	if val.IsNull() {
		ty := val.Type().AttributeType(name)
		return cty.NullVal(ty)
	}

	return val.GetAttr(name)
}
