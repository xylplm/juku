const supported = ['hongguo', 'huangju', 'yeguo', 'dsd'];

export function onlineSearchSources(allowed, selected = '') {
  const sources = Array.isArray(allowed) ? allowed : supported;
  return supported.filter(source => sources.includes(source) && (!selected || selected === source));
}

export function searchNextPages(value, allowed) {
  const result = {};
  if (!value || typeof value !== 'object' || Array.isArray(value)) return result;
  for (const [source, page] of Object.entries(value)) {
    if (allowed.includes(source) && Number.isInteger(page) && page >= 1 && page <= 1000000 && (source !== 'hongguo' || page === 1)) result[source] = page;
  }
  return result;
}
