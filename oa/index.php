<?php
require './conf.php';
require './barn.php';
$redirect=empty(@$_POST['r']) ? @$_GET['r'] : @$_POST['r'];
$rdpath=empty($redirect) ? $set['redirect'] : $set['redirect'].'?r='.base64_encode($redirect);/*这里把重定向网址用base64包装防止?一类请求符在传递过程中的丢失*/
jump('https://github.com/login/oauth/authorize',array(
   'response_type'=>'code',
   'client_id'=>$set['cid'],
   'redirect_uri'=>$rdpath
));
?>