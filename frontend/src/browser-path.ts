export function join(...parts:string[]):string{return parts.join('/').replace(/\/{2,}/g,'/').replace(/\/\.\//g,'/')}
export default{join};
