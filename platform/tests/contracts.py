"""Validate the JSON Schema keywords used by checked-in demo contracts."""
import re
import uuid

def validate(value, schema, document=None):
    document = document or schema
    allowed = {'$schema','$id','title','description','$ref','type','required','properties','additionalProperties','const','enum','minLength','maxLength','maxItems','items','pattern','format'}
    assert set(schema) <= allowed, 'Unsupported schema keywords: ' + str(set(schema)-allowed)
    if '$ref' in schema:
        target = document
        assert schema['$ref'].startswith('#/')
        for part in schema['$ref'][2:].split('/'):
            target = target[part]
        return validate(value,target,document)
    if 'type' in schema:
        types = {'object':dict,'array':list,'string':str,'boolean':bool,'integer':int,'number':(int,float)}
        assert isinstance(value,types[schema['type']]), (value,schema)
        if schema['type'] in ('integer','number'):
            assert not isinstance(value,bool)
    if 'const' in schema:
        assert type(value) is type(schema['const']) and value == schema['const'], (value,schema)
    if 'enum' in schema:
        assert value in schema['enum'], (value,schema)
    for key, comparison in [('minLength', lambda n,limit:n>=limit),('maxLength',lambda n,limit:n<=limit),('maxItems',lambda n,limit:n<=limit)]:
        if key in schema:
            assert comparison(len(value),schema[key]), (value,schema)
    if 'pattern' in schema:
        assert re.search(schema['pattern'],value), (value,schema)
    if schema.get('format') == 'uuid':
        assert str(uuid.UUID(value)) == value.lower(), value
    if isinstance(value,dict):
        assert set(schema.get('required',[])) <= set(value), (value,schema)
        properties = schema.get('properties',{})
        if schema.get('additionalProperties') is False:
            assert set(value) <= set(properties), (value,schema)
        for key, item in value.items():
            if key in properties:
                validate(item,properties[key],document)
    if isinstance(value,list) and 'items' in schema:
        for item in value:
            validate(item,schema['items'],document)
