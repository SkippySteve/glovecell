export const runHref = (id?: string) => '#/runs' + (id ? '/' + encodeURIComponent(id) : '');
