<?php
require './conf.php';
require './barn.php';
$code=$_GET['code'];
$redirect=@$_GET['r'];
$rdpath=empty($redirect) ? './../' : urldecode(base64_decode($redirect));
$request=post('https://github.com/login/oauth/access_token',array(
   'code'=>$code,
   'client_id'=>$set['cid'],
   'client_secret'=>$set['secret'],
   'redirect_uri'=>$set['redirect']
));
$pararr=array();
$paras=explode('&',$request);
foreach($paras as $v){
	$para=explode('=',$v);
	$pararr[$para[0]]=$para[1];
}
if(isset($pararr['error'])){
	echo 'Bad verification,please retry.';
}else{/*授权成功*/
	$token=$pararr['access_token'];
	$userd=get('https://api.github.com/user',$token);
	$detail=json_decode($userd,true);
	if(isset($detail['login'])){
	   $email=$detail['email'];
	   $name=$detail['name'] ? $detail['name'] : $detail['login'];
	   $id=$detail['id'];
	   $blog=$detail['blog'];
	   session_start();
	   $_SESSION['commentuser']=array(
	        'id'=>'github'.$id,
		    'blog'=>$blog,
		    'name'=>$name,
		    'email'=>$email
	   );
	   session_write_close();
	   header('Location: '.$rdpath);
	}else{
	   echo 'Something error happened,please contact the administrator.';
	}
}
?>