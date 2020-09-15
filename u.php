<?php
error_reporting(E_ALL ^ E_NOTICE ^ E_WARNING);
header('Content-type:text/json;charset=utf-8');
@session_start();
$rs=array();
$u=$_SESSION['commentuser'];
$rq=$_GET['q'];
$redirect=empty(@$_POST['r']) ? urldecode(@$_GET['r']) : @$_POST['r'];
$rdpath=empty($redirect) ? './' : $redirect;
if($rq=='destroy'){
	session_destroy();
	header('Location: '.$rdpath);
}
session_write_close();
if(isset($u)){
	$rs=$u;
	$rs['code']=1;
}else{
	$rs['code']=0;
}
echo json_encode($rs, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
?>