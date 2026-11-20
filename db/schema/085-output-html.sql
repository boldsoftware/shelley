-- output_html succeeds output_iframe: settings that turned output_iframe off
-- turn output_html off too, unless they say otherwise.
UPDATE profiles SET settings = json_set(settings, '$.tool_overrides.output_html', 'off')
WHERE CASE WHEN json_valid(settings) THEN
    json_extract(settings, '$.tool_overrides.output_iframe') = 'off'
    AND json_extract(settings, '$.tool_overrides.output_html') IS NULL
  END;
UPDATE conversations SET conversation_options = json_set(conversation_options, '$.tool_overrides.output_html', 'off')
WHERE CASE WHEN json_valid(conversation_options) THEN
    json_extract(conversation_options, '$.tool_overrides.output_iframe') = 'off'
    AND json_extract(conversation_options, '$.tool_overrides.output_html') IS NULL
  END;
