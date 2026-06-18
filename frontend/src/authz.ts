export type Role = 'admin' | 'operator' | 'viewer';

export const allRoles: Role[] = ['admin', 'operator', 'viewer'];
export const writeRoles: Role[] = ['admin', 'operator'];

export function hasRole(role: string | undefined, roles: readonly Role[]) {
  return Boolean(role && roles.includes(role as Role));
}

export function canManage(role: string | undefined) {
  return hasRole(role, writeRoles);
}

export function isAdmin(role: string | undefined) {
  return role === 'admin';
}
