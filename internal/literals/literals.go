package literals

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	mathrand "math/rand"
	"strconv"

	"golang.org/x/tools/go/ast/astutil"
)

var (
	usesUnsafe     bool
	universalTrue  = types.Universe.Lookup("true")
	universalFalse = types.Universe.Lookup("false")
)

func randObfuscator() obfuscator {
	randPos := mathrand.Intn(len(obfuscators))
	return obfuscators[randPos]
}

// Obfuscate replace literals with obfuscated lambda functions
func Obfuscate(files []*ast.File, info *types.Info, fset *token.FileSet, blacklist map[types.Object]struct{}) []*ast.File {
	pre := func(cursor *astutil.Cursor) bool {

		switch x := cursor.Node().(type) {
		case *ast.GenDecl:
			if x.Tok != token.CONST {
				return true
			}
			for _, spec := range x.Specs {
				spec, ok := spec.(*ast.ValueSpec)
				if !ok {
					return false
				}

				for _, name := range spec.Names {
					obj := info.ObjectOf(name)

					basic, ok := obj.Type().(*types.Basic)
					if !ok {
						// skip the block if it contains non basic types
						return false
					}

					if basic.Info()&types.IsUntyped != 0 {
						// skip the block if it contains untyped constants
						return false
					}

					// The object itself is blacklisted, e.g. a value that needs to be constant
					if _, ok := blacklist[obj]; ok {
						return false
					}
				}
			}

			x.Tok = token.VAR
			// constants are not possible if we want to obfuscate literals, therefore
			// move all constant blocks which only contain strings to variables
		}
		return true
	}

	post := func(cursor *astutil.Cursor) bool {
		switch x := cursor.Node().(type) {
		case *ast.CompositeLit:
			byteType := types.Universe.Lookup("byte").Type()

			if len(x.Elts) == 0 {
				return true
			}

			switch y := info.TypeOf(x.Type).(type) {
			case *types.Array:
				if y.Elem() != byteType {
					return true
				}

				data := make([]byte, y.Len())

				for i, el := range x.Elts {
					lit, ok := el.(*ast.BasicLit)
					if !ok {
						return true
					}

					value, err := strconv.Atoi(lit.Value)
					if err != nil {
						return true
					}

					data[i] = byte(value)
				}
				cursor.Replace(obfuscateByteArray(data, y.Len()))

			case *types.Slice:
				if y.Elem() != byteType {
					return true
				}

				data := make([]byte, 0, len(x.Elts))

				for _, el := range x.Elts {
					lit, ok := el.(*ast.BasicLit)
					if !ok {
						return true
					}

					value, err := strconv.Atoi(lit.Value)
					if err != nil {
						return true
					}

					data = append(data, byte(value))
				}
				cursor.Replace(obfuscateByteSlice(data))

			}

		case *ast.BasicLit:
			switch cursor.Name() {
			case "Values", "Rhs", "Value", "Args", "X", "Y", "Results":
			default:
				return true // we don't want to obfuscate imports etc.
			}

			switch x.Kind {
			case token.FLOAT, token.INT:
				obfuscateNumberLiteral(cursor, info)
			case token.STRING:
				typeInfo := info.TypeOf(x)
				if typeInfo != types.Typ[types.String] && typeInfo != types.Typ[types.UntypedString] {
					return true
				}
				value, err := strconv.Unquote(x.Value)
				if err != nil {
					panic(fmt.Sprintf("cannot unquote string: %v", err))
				}

				if len(value) == 0 {
					return true
				}

				cursor.Replace(obfuscateString(value))
			}
		case *ast.UnaryExpr:
			switch cursor.Name() {
			case "Values", "Rhs", "Value", "Args", "X":
			default:
				return true // we don't want to obfuscate imports etc.
			}

			obfuscateNumberLiteral(cursor, info)
		case *ast.Ident:
			obj := info.ObjectOf(x)
			if obj == nil {
				return true
			}

			if obj == universalTrue || obj == universalFalse {
				cursor.Replace(obfuscateBool(x.Name == "true"))
			}
		}

		return true
	}

	for i := range files {
		usesUnsafe = false
		files[i] = astutil.Apply(files[i], pre, post).(*ast.File)
		if usesUnsafe {
			astutil.AddImport(fset, files[i], "unsafe")
		}
	}
	return files
}

func obfuscateString(data string) *ast.CallExpr {
	obfuscator := randObfuscator()
	block := obfuscator.obfuscate([]byte(data))

	block.List = append(block.List, returnStmt(callExpr(ident("string"), ident("data"))))

	return lambdaCall(ident("string"), block)
}

func obfuscateByteSlice(data []byte) *ast.CallExpr {
	obfuscator := randObfuscator()
	block := obfuscator.obfuscate(data)
	block.List = append(block.List, returnStmt(ident("data")))
	return lambdaCall(&ast.ArrayType{Elt: ident("byte")}, block)
}

func obfuscateByteArray(data []byte, length int64) *ast.CallExpr {
	obfuscator := randObfuscator()
	block := obfuscator.obfuscate(data)

	arrayType := &ast.ArrayType{
		Len: intLiteral(strconv.Itoa(int(length))),
		Elt: ident("byte"),
	}

	sliceToArray := []ast.Stmt{
		&ast.DeclStmt{
			Decl: &ast.GenDecl{
				Tok: token.VAR,
				Specs: []ast.Spec{&ast.ValueSpec{
					Names: []*ast.Ident{ident("newdata")},
					Type:  arrayType,
				}},
			},
		},
		&ast.RangeStmt{
			Key: ident("i"),
			Tok: token.DEFINE,
			X:   ident("newdata"),
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.AssignStmt{
					Lhs: []ast.Expr{indexExpr("newdata", ident("i"))},
					Tok: token.ASSIGN,
					Rhs: []ast.Expr{indexExpr("data", ident("i"))},
				},
			}},
		},
		returnStmt(ident("newdata")),
	}

	block.List = append(block.List, sliceToArray...)

	return lambdaCall(arrayType, block)
}

func obfuscateBool(data bool) *ast.BinaryExpr {
	var dataUint64 uint64 = 0
	if data {
		dataUint64 = 1
	}

	intType := intTypes[types.Typ[types.Uint8]]

	return &ast.BinaryExpr{
		X:  genObfuscateInt(dataUint64, intType),
		Op: token.EQL,
		Y:  intLiteral("1"),
	}
}

// ConstBlacklist blacklist identifieres used in constant expressions
func ConstBlacklist(node ast.Node, info *types.Info, blacklist map[types.Object]struct{}) {
	blacklistObjects := func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok {
			return true
		}

		obj := info.ObjectOf(ident)
		blacklist[obj] = struct{}{}

		return true
	}

	switch x := node.(type) {
	// in a slice or array composite literal all explicit keys must be constant representable
	case *ast.CompositeLit:
		if _, ok := x.Type.(*ast.ArrayType); !ok {
			break
		}
		for _, elt := range x.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				ast.Inspect(kv.Key, blacklistObjects)
			}
		}
	// in an array type the length must be a constant representable
	case *ast.ArrayType:
		if x.Len != nil {
			ast.Inspect(x.Len, blacklistObjects)
		}
	// in a const declaration all values must be constant representable
	case *ast.GenDecl:
		if x.Tok != token.CONST {
			break
		}
		for _, spec := range x.Specs {
			spec := spec.(*ast.ValueSpec)

			for _, val := range spec.Values {
				ast.Inspect(val, blacklistObjects)
			}
		}
	}
}
