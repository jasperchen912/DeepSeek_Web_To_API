'use strict';

const {
  toStringSafe,
} = require('./state');
const {
  parseMarkupToolCalls,
  stripFencedCodeBlocks,
  containsToolMarkupSyntaxOutsideIgnored,
  containsToolCallWrapperSyntaxOutsideIgnored,
  sanitizeLooseCDATA,
} = require('./parse_payload');

function extractToolNames(tools) {
  if (!Array.isArray(tools) || tools.length === 0) {
    return [];
  }
  const out = [];
  const seen = new Set();
  for (const t of tools) {
    if (!t || typeof t !== 'object') {
      continue;
    }
    const fn = t.function && typeof t.function === 'object' ? t.function : t;
    const name = toStringSafe(fn.name);
    if (!name || seen.has(name)) {
      continue;
    }
    seen.add(name);
    out.push(name);
  }
  return out;
}

function parseToolCalls(text, toolNames) {
  return parseToolCallsDetailed(text, toolNames).calls;
}

function parseToolCallsDetailed(text, toolNames) {
  return parseDetailedMarkupOnly(text, toolNames);
}

function parseStandaloneToolCalls(text, toolNames) {
  return parseStandaloneToolCallsDetailed(text, toolNames).calls;
}

function parseStandaloneToolCallsDetailed(text, toolNames) {
  return parseDetailedMarkupOnly(text, toolNames);
}

function parseDetailedMarkupOnly(text, toolNames) {
  const result = emptyParseResult();
  const trimmed = toStringSafe(text).trim();
  if (!trimmed) {
    result.rejectReason = 'empty_input';
    return result;
  }
  result.sawToolCallSyntax = looksLikeToolCallSyntax(trimmed);
  result.variant = classifyToolCallVariant(trimmed);
  if (!result.sawToolCallSyntax) {
    result.rejectReason = 'no_tool_call_wrapper';
    return result;
  }
  const stripped = stripFencedCodeBlocks(trimmed).trim();
  if (!stripped || !looksLikeToolCallSyntax(stripped)) {
    result.variant = 'fenced_tool_call_example';
    result.rejectReason = 'no_tool_call_outside_fence';
    return result;
  }

  const candidate = sanitizeLooseCDATA(stripped);
  const repairedCandidate = candidate !== stripped;
  let parsed = parseMarkupToolCalls(candidate);
  let parsedRepairedCandidate = repairedCandidate && parsed.length > 0;
  if (parsed.length === 0 && repairedCandidate) {
    parsed = parseMarkupToolCalls(stripped);
    parsedRepairedCandidate = false;
  }
  if (parsed.length === 0) {
    result.rejectReason = 'parse_failed';
    return result;
  }
  if (parsedRepairedCandidate) {
    result.repaired = true;
    result.variant = withRepairVariant(result.variant, 'loose_cdata');
  }

  result.sawToolCallSyntax = true;
  const filtered = filterToolCallsDetailed(parsed, toolNames);
  result.calls = filtered.calls;
  result.rejectedToolNames = filtered.rejectedToolNames;
  result.rejectedByPolicy = filtered.rejectedToolNames.length > 0 && filtered.calls.length === 0;
  if (result.rejectedByPolicy) {
    result.rejectReason = 'policy_rejected';
  } else if (filtered.calls.length === 0) {
    result.rejectReason = 'empty_arguments';
  }
  return result;
}

function emptyParseResult() {
  return {
    calls: [],
    sawToolCallSyntax: false,
    rejectedByPolicy: false,
    rejectedToolNames: [],
    repaired: false,
    variant: '',
    rejectReason: '',
  };
}

function filterToolCallsDetailed(parsed, toolNames) {
  const calls = [];
  for (const tc of parsed) {
    if (!tc || !tc.name) {
      continue;
    }
    const input = tc.input && typeof tc.input === 'object' && !Array.isArray(tc.input) ? tc.input : {};
    if (Object.keys(input).length > 0 && !toolCallInputHasMeaningfulValue(input)) {
      continue;
    }
    calls.push({
      name: tc.name,
      input,
    });
  }
  return { calls, rejectedToolNames: [] };
}

function toolCallInputHasMeaningfulValue(value) {
  if (value == null) {
    return false;
  }
  if (typeof value === 'string') {
    return value.trim() !== '';
  }
  if (Array.isArray(value)) {
    if (value.length === 0) {
      return false;
    }
    return value.some((item) => toolCallInputHasMeaningfulValue(item));
  }
  if (typeof value === 'object') {
    const values = Object.values(value);
    if (values.length === 0) {
      return false;
    }
    return values.some((item) => toolCallInputHasMeaningfulValue(item));
  }
  return true;
}

function looksLikeToolCallSyntax(text) {
  const styles = containsToolCallWrapperSyntaxOutsideIgnored(text);
  return styles.dsml || styles.canonical;
}

function classifyToolCallVariant(text) {
  const styles = containsToolMarkupSyntaxOutsideIgnored(text);
  if (styles.dsml && styles.canonical) {
    return 'mixed_dsml_xml';
  }
  if (styles.dsml) {
    return 'dsml';
  }
  if (styles.canonical) {
    return 'canonical_xml';
  }
  return 'unknown';
}

function withRepairVariant(base, repair) {
  const repairName = toStringSafe(repair).trim();
  if (!repairName) {
    return base;
  }
  const variant = toStringSafe(base).trim();
  if (!variant || variant === 'unknown') {
    return `repaired_${repairName}`;
  }
  if (variant.includes(`repaired_${repairName}`)) {
    return variant;
  }
  return `${variant}+repaired_${repairName}`;
}

module.exports = {
  extractToolNames,
  parseToolCalls,
  parseToolCallsDetailed,
  parseStandaloneToolCalls,
  parseStandaloneToolCallsDetailed,
};
