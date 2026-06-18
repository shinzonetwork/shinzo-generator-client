package schema

// GetSchema returns the full concatenated GraphQL schema from all collection files.
func GetSchema() (string, error) {
	return LoadSchemaSDL()
}

// GetSchemaForChain returns the schema with collection names adapted for the given chain prefix.
func GetSchemaForChain(prefix string) (string, error) {
	return LoadSchemaSDLForChain(prefix)
}
